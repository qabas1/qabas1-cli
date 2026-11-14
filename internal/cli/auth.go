package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/hint"
	"github.com/qabasone/qabasone-cli/internal/release"
	"github.com/spf13/cobra"
)

func newAuthCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage GitHub and GHCR credentials for QabasOne installs",
		Long:  "Save and inspect credentials so sudo qabas install/deploy works without re-exporting env.",
		Example: `  export GITHUB_TOKEN="$(gh auth token)"
  sudo -E qabas auth login
  sudo qabas auth status
  qabas login`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(
		newAuthStatusCmd(f),
		newAuthSetCmd(f),
		newAuthGetCmd(f),
		newAuthUnsetCmd(f),
		newAuthLoginCmd(f),
		newAuthLogoutCmd(f),
	)
	return cmd
}

func newAuthStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether GitHub and GHCR credentials are available",
		Example: `  qabas auth status
  qabas auth status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st := release.Status()
			if f.JSON {
				return e.Out.Encode(st)
			}
			printCredLine(e, "github", st.GitHubOK, st.GitHubSource, st.GitHubSystemPath, "")
			userDetail := ""
			if st.GHCRUsername != "" {
				userDetail = "username=" + st.GHCRUsername
				if st.GHCRUsernameSource != "" {
					userDetail += " (from " + st.GHCRUsernameSource + ")"
				}
			}
			printCredLine(e, "ghcr", st.GHCROK, st.GHCRSource, st.GHCRSystemPath, userDetail)
			if !st.OK {
				e.Out.Println("")
				e.Out.Println("Install/deploy needs GitHub + GHCR credentials.")
				e.Out.Println(hint.Localize("Run: export GITHUB_TOKEN=\"$(gh auth token)\""))
				e.Out.Println("     " + hint.CmdEnv("qabas auth login"))
			}
			return nil
		},
	}
}

func printCredLine(e *env, kind string, ok bool, source, systemPath, extra string) {
	if ok {
		line := fmt.Sprintf("%s: OK (from %s)", kind, source)
		if extra != "" {
			line += "; " + extra
		}
		e.Out.Println(line)
		return
	}
	e.Out.Printf("%s: missing (system path: %s)\n", kind, systemPath)
	if extra != "" {
		e.Out.Printf("  %s\n", extra)
	}
}

func newAuthSetCmd(f *flags) *cobra.Command {
	var tokenFile string
	var tokenFlag string
	var username string
	cmd := &cobra.Command{
		Use:               "set <github|ghcr>",
		Short:             "Create or update a saved credential",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAuthKind,
		Long: `Saves a credential under the qabas config dir (/etc/qabasone on Linux,
%ProgramData%\QabasOne\etc on Windows) so install/deploy work without re-exporting env.

github: PAT with repo read for private QabasOne GitHub repos
ghcr:   PAT with read:packages for ghcr.io/qabasone/* images, plus --username

Looks for a token in this order:
  1. --token / --token-file
  2. kind-specific env (GITHUB_TOKEN / GHCR_TOKEN)
  3. already-discovered sources (github only: gh auth token)
  4. interactive prompt (unless --non-interactive)`,
		Example: `  sudo -E qabas auth set github
  sudo -E qabas auth set ghcr --username myuser`,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := release.ParseKind(args[0])
			if err != nil {
				return exitcode.New(exitcode.InvalidInput, "%v", err)
			}
			token, err := resolveTokenInput(f, kind, tokenFlag, tokenFile, cmd)
			if err != nil {
				return err
			}
			e := load(f)
			switch kind {
			case release.KindGitHub:
				path, err := release.SaveGitHubToken(token)
				if err != nil {
					return exitcode.Wrap(exitcode.InvalidInput, err)
				}
				e.Out.Printf("Saved github token to %s\n", path)
			case release.KindGHCR:
				user, err := resolveUsername(f, username, cmd)
				if err != nil {
					return err
				}
				path, err := release.SaveGHCRToken(token)
				if err != nil {
					return exitcode.Wrap(exitcode.InvalidInput, err)
				}
				upath, err := release.SaveGHCRUsername(user)
				if err != nil {
					return exitcode.Wrap(exitcode.InvalidInput, err)
				}
				e.Out.Printf("Saved ghcr token to %s\n", path)
				e.Out.Printf("Saved ghcr username %q to %s\n", user, upath)
			}
			e.Out.Println("Check: " + hint.Cmd("qabas auth status"))
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read token from file")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "token value (prefer --token-file or env)")
	cmd.Flags().StringVar(&username, "username", "", "GHCR docker username (GitHub username); required for ghcr")
	return cmd
}

func resolveTokenInput(f *flags, kind release.Kind, tokenFlag, tokenFile string, cmd *cobra.Command) (string, error) {
	token := strings.TrimSpace(tokenFlag)
	if token == "" && tokenFile != "" {
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", exitcode.New(exitcode.InvalidInput, "read token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		switch kind {
		case release.KindGitHub:
			token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
			if token == "" {
				token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
			}
			if token == "" {
				token = release.GitHubToken()
			}
		case release.KindGHCR:
			token = strings.TrimSpace(os.Getenv("GHCR_TOKEN"))
			if token == "" {
				token = strings.TrimSpace(os.Getenv("QABAS_GHCR_TOKEN"))
			}
			if token == "" {
				token = release.GHCRToken()
			}
		}
	}
	if token == "" && !f.NonInteractive {
		label := "GitHub token (repo read)"
		if kind == release.KindGHCR {
			label = "GHCR token (read:packages)"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: ", label)
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil {
			return "", exitcode.New(exitcode.InvalidInput, "read token: %w", err)
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		h := "export GITHUB_TOKEN=\"$(gh auth token)\" && sudo -E qabas auth login"
		if kind == release.KindGHCR {
			h = "export GHCR_TOKEN=\"…\" && sudo -E qabas auth set ghcr --username YOU"
		}
		return "", exitcode.NewHint(exitcode.InvalidInput, hint.Localize(h), "no token found")
	}
	return token, nil
}

func newAuthGetCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:               "get <github|ghcr>",
		Short:             "Show one credential status (never prints the secret)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAuthKind,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := release.ParseKind(args[0])
			if err != nil {
				return exitcode.New(exitcode.InvalidInput, "%v", err)
			}
			e := load(f)
			st := release.Status()
			switch kind {
			case release.KindGitHub:
				if f.JSON {
					return e.Out.Encode(map[string]any{
						"kind": "github", "ok": st.GitHubOK, "source": st.GitHubSource,
						"systemPath": st.GitHubSystemPath,
					})
				}
				printCredLine(e, "github", st.GitHubOK, st.GitHubSource, st.GitHubSystemPath, "")
			case release.KindGHCR:
				if f.JSON {
					return e.Out.Encode(map[string]any{
						"kind": "ghcr", "ok": st.GHCROK, "source": st.GHCRSource,
						"systemPath": st.GHCRSystemPath, "username": st.GHCRUsername,
						"usernameSource": st.GHCRUsernameSource,
					})
				}
				extra := ""
				if st.GHCRUsername != "" {
					extra = "username=" + st.GHCRUsername
				}
				printCredLine(e, "ghcr", st.GHCROK, st.GHCRSource, st.GHCRSystemPath, extra)
			}
			return nil
		},
	}
}

func newAuthUnsetCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:               "unset <github|ghcr>",
		Short:             "Remove a saved system credential file",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAuthKind,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, err := release.ParseKind(args[0])
			if err != nil {
				return exitcode.New(exitcode.InvalidInput, "%v", err)
			}
			e := load(f)
			if err := release.UnsetKind(kind); err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			switch kind {
			case release.KindGitHub:
				e.Out.Printf("Removed %s (if it existed)\n", release.SystemTokenPath())
			case release.KindGHCR:
				e.Out.Printf("Removed %s and %s (if they existed)\n",
					release.SystemGHCRTokenPath(), release.SystemGHCRUsernamePath())
			}
			return nil
		},
	}
}

func resolveUsername(f *flags, usernameFlag string, cmd *cobra.Command) (string, error) {
	user := strings.TrimSpace(usernameFlag)
	if user == "" {
		user = strings.TrimSpace(os.Getenv("GHCR_USERNAME"))
	}
	if user == "" {
		user = strings.TrimSpace(os.Getenv("GITHUB_USER"))
	}
	if user == "" {
		user = release.GHCRUsername()
	}
	if user == "" && !f.NonInteractive {
		fmt.Fprint(cmd.ErrOrStderr(), "GHCR username (GitHub username): ")
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil {
			return "", exitcode.New(exitcode.InvalidInput, "read username: %w", err)
		}
		user = strings.TrimSpace(line)
	}
	if user == "" {
		return "", exitcode.NewHint(exitcode.InvalidInput,
			"pass --username, set GHCR_USERNAME, or install gh CLI",
			"ghcr username required")
	}
	return user, nil
}

func newAuthLoginCmd(f *flags) *cobra.Command {
	var tokenFile string
	var tokenFlag string
	var ghcrToken string
	var ghcrTokenFile string
	var username string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save GitHub + GHCR credentials for sudo-safe install/deploy",
		Long: `Saves one GitHub personal access token for both private repos and ghcr.io pulls
(so "sudo qabas install" works without re-exporting env).

Looks for a token in: --token / --token-file, GITHUB_TOKEN, gh auth token, or prompt.
Username is auto-detected via gh / GHCR_USERNAME / GITHUB_USER when possible.

Optional overrides: --ghcr-token (different registry PAT), --username.

Also available as: qabas login`,
		Example: `  export GITHUB_TOKEN="$(gh auth token)"
  sudo -E qabas auth login
  sudo -E qabas login`,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := resolveTokenInput(f, release.KindGitHub, tokenFlag, tokenFile, cmd)
			if err != nil {
				return err
			}
			e := load(f)
			path, err := release.SaveGitHubToken(token)
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			e.Out.Printf("Saved github token to %s\n", path)

			regToken := strings.TrimSpace(ghcrToken)
			if regToken == "" && ghcrTokenFile != "" {
				b, err := os.ReadFile(ghcrTokenFile)
				if err != nil {
					return exitcode.New(exitcode.InvalidInput, "read ghcr token file: %w", err)
				}
				regToken = strings.TrimSpace(string(b))
			}
			if regToken == "" {
				regToken = strings.TrimSpace(os.Getenv("GHCR_TOKEN"))
			}
			if regToken == "" {
				regToken = strings.TrimSpace(os.Getenv("QABAS_GHCR_TOKEN"))
			}
			if regToken == "" {
				regToken = token
			}
			user, err := resolveUsername(f, username, cmd)
			if err != nil {
				return err
			}
			rpath, err := release.SaveGHCRToken(regToken)
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			upath, err := release.SaveGHCRUsername(user)
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			e.Out.Printf("Saved ghcr token to %s\n", rpath)
			e.Out.Printf("Saved ghcr username %q to %s\n", user, upath)
			e.Out.Println("You can now run: " + hint.Cmd("qabas install"))
			e.Out.Println("Check: " + hint.Cmd("qabas auth status"))
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "read GitHub token from file")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "GitHub token value (prefer --token-file or env)")
	cmd.Flags().StringVar(&ghcrToken, "ghcr-token", "", "GHCR token (default: same as github token)")
	cmd.Flags().StringVar(&ghcrTokenFile, "ghcr-token-file", "", "read GHCR token from file")
	cmd.Flags().StringVar(&username, "username", "", "GHCR docker username (auto-detected when possible)")
	_ = cmd.Flags().Bool("ghcr", true, "deprecated: login always saves GHCR credentials")
	_ = cmd.Flags().MarkDeprecated("ghcr", "login always saves GitHub and GHCR credentials")
	return cmd
}

func newAuthLogoutCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		Short:   "Remove saved system GitHub and GHCR credential files",
		Example: `  sudo qabas auth logout`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if err := release.UnsetGitHubToken(); err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			if err := release.UnsetGHCR(); err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			e.Out.Printf("Removed %s, %s, and %s (if they existed)\n",
				release.SystemTokenPath(), release.SystemGHCRTokenPath(), release.SystemGHCRUsernamePath())
			return nil
		},
	}
}
