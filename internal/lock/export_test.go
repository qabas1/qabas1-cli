package lock

import "time"

type ProcInfo = procInfo

var ParseHolder = parseHolder

func Evaluate(h Holder, p ProcInfo, now time.Time) Status {
	s := Status{Held: true, Holder: &h, Alive: p.Alive, State: p.State, Signalable: p.Alive}
	evaluate(&s, p, now)
	return s
}

// SetForTest shortens timings and replaces the qabas-process check; the returned func restores them.
func SetForTest(heartbeat, term, kill time.Duration, isQabas func(string) bool) func() {
	oh, ot, ok, oq := heartbeatEvery, termGrace, killGrace, isQabasExe
	heartbeatEvery, termGrace, killGrace, isQabasExe = heartbeat, term, kill, isQabas
	return func() { heartbeatEvery, termGrace, killGrace, isQabasExe = oh, ot, ok, oq }
}

var IsQabasExe = isQabasExe
