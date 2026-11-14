package cli

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/relay"
	"github.com/spf13/cobra"
)

func newRelayCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "WireGuard-over-TLS relay for replicas whose network blocks WireGuard",
		Long: `Some networks drop WireGuard while allowing TLS. For such replicas the writer runs
a relay (qabas-relay.service, TLS on TCP 8443 by default) and the replica runs a
client (qabas-relay-client.service) that carries its WireGuard packets over TLS.

` + "`qabas replica invite` and `qabas replica add`" + ` set this up automatically when direct
WireGuard gets no handshake (or with --relay). These commands are what the services run.`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newRelayServeCmd(f), newRelayConnectCmd(f), newRelayStatusCmd(f))
	return cmd
}

// relayDir is where the writer keeps the relay certificate and a replica its client config.
func relayDir(p paths.Paths) string { return filepath.Join(p.ConfigDir, "relay") }

func relayClientConfig(p paths.Paths) string { return filepath.Join(relayDir(p), "client.env") }

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func newRelayServeCmd(f *flags) *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the writer side of the relay (systemd: qabas-relay.service)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := paths.Resolve()
			c, err := cluster.Load(p)
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			if listen == "" {
				listen = ":" + strconv.Itoa(c.RelayPortOrDefault())
			}
			dir := relayDir(p)
			lg := log.New(os.Stderr, "", 0)
			ctx, cancel := signalContext()
			defer cancel()
			return relay.Serve(ctx, relay.ServerConfig{
				Listen:   listen,
				CertFile: relay.CertFile(dir),
				KeyFile:  relay.KeyFile(dir),
				WGAddr:   "127.0.0.1:" + strconv.Itoa(c.WGPort),
				Token: func(node string) (string, error) {
					return cluster.ReadSecret(p, node, cluster.SecretRelayToken)
				},
				Logf: lg.Printf,
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "", "TCP listen address (default :<relayPort from cluster.yaml>)")
	return cmd
}

// readRelayClientConfig parses client.env (KEY=VALUE lines).
func readRelayClientConfig(path string) (relay.ClientConfig, error) {
	fh, err := os.Open(path)
	if err != nil {
		return relay.ClientConfig{}, err
	}
	defer fh.Close()
	kv := map[string]string{}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	if err := sc.Err(); err != nil {
		return relay.ClientConfig{}, err
	}
	cfg := relay.ClientConfig{
		Server: kv["RELAY_SERVER"], CertSHA256: kv["RELAY_CERT_SHA256"], Node: kv["RELAY_NODE"], Token: kv["RELAY_TOKEN"],
		Listen: kv["RELAY_LISTEN"],
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:" + strconv.Itoa(cluster.RelayLocalPort)
	}
	for k, v := range map[string]string{"RELAY_SERVER": cfg.Server, "RELAY_CERT_SHA256": cfg.CertSHA256, "RELAY_NODE": cfg.Node, "RELAY_TOKEN": cfg.Token} {
		if v == "" {
			return cfg, fmt.Errorf("%s: %s is missing", path, k)
		}
	}
	return cfg, nil
}

func newRelayConnectCmd(f *flags) *cobra.Command {
	var config string
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Run the replica side of the relay (systemd: qabas-relay-client.service)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if config == "" {
				config = relayClientConfig(paths.Resolve())
			}
			cfg, err := readRelayClientConfig(config)
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			cfg.Logf = log.New(os.Stderr, "", 0).Printf
			ctx, cancel := signalContext()
			defer cancel()
			return relay.Connect(ctx, cfg)
		},
	}
	cmd.Flags().StringVar(&config, "config", "", "client config (default /etc/qabasone/relay/client.env)")
	return cmd
}

func newRelayStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the relay role of this machine and test the relay connection",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			found := false
			if fp, err := relay.FingerprintFile(relay.CertFile(relayDir(e.Paths))); err == nil {
				found = true
				port := cluster.DefaultRelayPort
				if c, err := cluster.Load(e.Paths); err == nil {
					port = c.RelayPortOrDefault()
				}
				e.Out.Printf("relay server: TCP %d, certificate sha256 %s\n", port, fp)
			}
			if cfg, err := readRelayClientConfig(relayClientConfig(e.Paths)); err == nil {
				found = true
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
				defer cancel()
				c, _, err := relay.Dial(ctx, cfg)
				if err != nil {
					return exitcode.New(exitcode.Network, "relay client for %s: cannot reach %s: %v", cfg.Node, cfg.Server, err)
				}
				c.Close()
				e.Out.Printf("relay client: %s authenticated to %s (WireGuard via %s)\n", cfg.Node, cfg.Server, cfg.Listen)
			}
			if !found {
				e.Out.Printf("this machine is neither a relay server nor a relay client\n")
			}
			return nil
		},
	}
}
