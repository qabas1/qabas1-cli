package guide

import "fmt"

// Overrides replace detected values in generated commands.
type Overrides struct {
	Name   string
	Host   string
	Writer string
}

// Vars are the values substituted into commands; unknown ones are <placeholders>.
type Vars struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	HostKey string `json:"hostKey"`
	Writer  string `json:"writer"`
	Site    string `json:"site"`
	Replica string `json:"replica"`
	Offsite string `json:"offsite"`
	Backup  string `json:"backup"`
}

// NewVars derives command values from facts and overrides.
func NewVars(f Facts, o Overrides) Vars {
	v := Vars{
		Name: "<name>", Host: "<this-ip>", HostKey: "SHA256:<fingerprint>",
		Writer: "<writer>", Site: "<site>", Replica: "<replica>",
		Offsite: "<replica>", Backup: "<backup-id>",
	}
	if f.Detected {
		switch {
		case f.Role == RoleReplica && f.Self != "":
			v.Name = f.Self
		case f.Role == RoleFresh:
			if n := NodeNameFromHostname(f.Hostname); n != "" {
				v.Name = n
			}
		}
		// On a writer, --host / --host-key describe another machine, so keep placeholders.
		if !f.isWriter() {
			if len(f.IPs) > 0 {
				v.Host = f.IPs[0]
			}
			if f.HostKey != "" {
				v.HostKey = f.HostKey
			}
		} else {
			v.Host = "<replica-ip>"
		}
		switch {
		case f.Writer != "":
			v.Writer = f.Writer
		case f.Role == RoleSingle:
			if n := NodeNameFromHostname(f.Hostname); n != "" {
				v.Writer = n
			}
		}
		for _, n := range f.Standbys() {
			if v.Replica == "<replica>" || (n.Promotable && !f.Node(v.Replica).Promotable) {
				v.Replica = n.Name
			}
		}
		if f.Role == RoleReplica && f.Self != "" {
			v.Replica = f.Self
		}
		if f.Offsite != "" {
			v.Offsite = f.Offsite
		} else if v.Replica != "<replica>" {
			v.Offsite = v.Replica
		}
		if f.LatestBackup != "" {
			v.Backup = f.LatestBackup
		}
	}
	if o.Name != "" {
		v.Name = o.Name
	}
	if o.Host != "" {
		v.Host = o.Host
	}
	if o.Writer != "" {
		v.Writer = o.Writer
	}
	if v.Name != "<name>" {
		v.Site = v.Name
	}
	return v
}

func done(reason string) Status    { return Status{State: Done, Reason: reason} }
func todo(reason string) Status    { return Status{State: Todo, Reason: reason} }
func blocked(reason string) Status { return Status{State: Blocked, Reason: reason} }

// check is Done when ok, otherwise Blocked with why; Unknown without detection.
func check(f Facts, ok bool, why string) Status {
	if !f.Detected {
		return Status{}
	}
	if ok {
		return done("")
	}
	return blocked(why)
}

// progress is Done when ok, otherwise Todo; Unknown without detection.
func progress(f Facts, ok bool, why string) Status {
	if !f.Detected {
		return Status{}
	}
	if ok {
		return done(why)
	}
	return todo("")
}

// optional is Done when ok, otherwise Todo with why (something qabas does for you);
// Unknown without detection.
func optional(f Facts, ok bool, why string) Status {
	if !f.Detected {
		return Status{}
	}
	if ok {
		return done("")
	}
	return todo(why)
}

func (f Facts) isWriter() bool { return f.Role == RoleWriter || f.Role == RoleSingle }

// writerWhere labels writer-side sections; it is "this machine" when we are the writer.
func writerWhere(f Facts) string {
	if f.Detected && f.isWriter() {
		return WhereHere + " (writer)"
	}
	return WhereWriter
}

func mib(n int64) string {
	if n >= 1024 {
		return fmt.Sprintf("%.1f GiB", float64(n)/1024)
	}
	return fmt.Sprintf("%d MiB", n)
}
