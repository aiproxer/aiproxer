package app

import "time"

// Ticker is a stoppable pulse source used for tick/renew loops.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type stdTicker struct {
	t *time.Ticker
}

func (t stdTicker) C() <-chan time.Time { return t.t.C }
func (t stdTicker) Stop()               { t.t.Stop() }

func defaultNewTicker(d time.Duration) Ticker {
	return stdTicker{t: time.NewTicker(d)}
}
