package runtimehost

func (g *Generation) Close() error {
	if g == nil {
		return nil
	}
	g.closeMu.Lock()
	defer g.closeMu.Unlock()

	st, _ := unpackLease(g.word.Load())
	if st == GenClosed {
		return ErrAlreadyClosed
	}
	if st == GenFailed {
		g.payloadMu.Lock()
		empty := g.owned == nil && g.requestPlane == nil
		g.payloadMu.Unlock()
		if empty {
			return ErrAlreadyClosed
		}
		return ErrIllegalTransition
	}
	if st != GenClosing {
		return ErrIllegalTransition
	}

	g.payloadMu.Lock()
	owned := g.owned
	g.payloadMu.Unlock()

	g.closeCount.Add(1)
	if owned != nil {
		if err := owned.Close(); err != nil {
			return err
		}
	}

	g.payloadMu.Lock()
	g.owned = nil
	g.requestPlane = nil
	g.payloadMu.Unlock()
	for {
		cur := g.word.Load()
		_, refs := unpackLease(cur)
		if g.word.CompareAndSwap(cur, packLease(GenClosed, refs)) {
			break
		}
	}
	return nil
}

func (g *Generation) Discard() error {
	if g == nil {
		return nil
	}
	g.closeMu.Lock()
	defer g.closeMu.Unlock()

	g.payloadMu.Lock()
	firstTransition := false
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		switch st {
		case GenPreparing, GenPrepared:
			if !g.word.CompareAndSwap(cur, packLease(GenFailed, refs)) {
				continue
			}
			firstTransition = true
		case GenFailed:
		default:
			g.payloadMu.Unlock()
			return ErrIllegalTransition
		}
		break
	}

	owned := g.owned
	plane := g.requestPlane
	g.owned = nil
	g.requestPlane = nil
	g.payloadMu.Unlock()

	if !firstTransition && owned == nil && plane == nil {
		return ErrAlreadyClosed
	}

	g.closeCount.Add(1)
	if owned == nil {
		return nil
	}
	if err := owned.Close(); err != nil {
		g.payloadMu.Lock()
		if g.owned == nil && g.requestPlane == nil {
			g.owned = owned
			g.requestPlane = plane
		}
		g.payloadMu.Unlock()
		return err
	}
	return nil
}
