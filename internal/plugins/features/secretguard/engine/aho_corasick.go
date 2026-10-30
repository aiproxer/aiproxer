package engine

type ahoCorasick struct {
	nodes []acNode
}

type acNode struct {
	next [256]int
	fail int
	out  []acOut
}

type acOut struct {
	entryIdx int
	length   int
}

func buildAhoCorasick(entries []catalogEntry) *ahoCorasick {
	ac := &ahoCorasick{nodes: []acNode{{}}}
	for i := range entries {
		v := entries[i].value
		if len(v) == 0 {
			continue
		}
		state := 0
		for _, c := range v {
			nxt := ac.nodes[state].next[c]
			if nxt == 0 {
				nxt = len(ac.nodes)
				ac.nodes = append(ac.nodes, acNode{})
				ac.nodes[state].next[c] = nxt
			}
			state = nxt
		}
		ac.nodes[state].out = append(ac.nodes[state].out, acOut{entryIdx: i, length: len(v)})
	}

	queue := make([]int, 0, len(ac.nodes))
	for b := range 256 {
		if nxt := ac.nodes[0].next[b]; nxt != 0 {
			queue = append(queue, nxt)
		}
	}
	for head := 0; head < len(queue); head++ {
		r := queue[head]
		for b := range 256 {
			s := ac.nodes[r].next[b]
			if s == 0 {
				continue
			}
			queue = append(queue, s)
			f := ac.transition(ac.nodes[r].fail, byte(b))
			ac.nodes[s].fail = f
			if len(ac.nodes[f].out) > 0 {
				ac.nodes[s].out = append(ac.nodes[s].out, ac.nodes[f].out...)
			}
		}
	}
	return ac
}

func (ac *ahoCorasick) transition(state int, c byte) int {
	for state != 0 && ac.nodes[state].next[c] == 0 {
		state = ac.nodes[state].fail
	}
	if nxt := ac.nodes[state].next[c]; nxt != 0 {
		return nxt
	}
	return 0
}

func (ac *ahoCorasick) findAll(input []byte) []matchHit {
	if ac == nil || len(ac.nodes) == 0 || len(input) == 0 {
		return nil
	}
	var out []matchHit
	state := 0
	for i := range input {
		state = ac.transition(state, input[i])
		for _, o := range ac.nodes[state].out {
			if out == nil {
				out = make([]matchHit, 0, 8)
			}
			out = append(out, matchHit{
				start:    i - o.length + 1,
				length:   o.length,
				entryIdx: o.entryIdx,
			})
		}
	}
	return out
}
