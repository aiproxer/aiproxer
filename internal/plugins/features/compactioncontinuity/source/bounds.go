package source

import (
	"fmt"
	"sort"
)

func boundEntries(entries []Entry, cfg Config) []Entry {
	entries = cloneEntries(entries)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].priority != entries[j].priority {
			return entries[i].priority < entries[j].priority
		}
		if entries[i].ItemIndex != entries[j].ItemIndex {
			return entries[i].ItemIndex < entries[j].ItemIndex
		}
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].ItemID < entries[j].ItemID
	})
	if len(entries) > cfg.MaxEntries {
		entries = entries[:cfg.MaxEntries]
	}
	out := make([]Entry, 0, len(entries))
	used := 0
	for _, entry := range entries {
		if entry.Carrier != nil {
			entry.Carrier.Payload = truncateUTF8(entry.Carrier.Payload, cfg.MaxCarrierBytes)
		} else {
			entry.Text = truncateEntryText(entry.Text, entry.Untrusted, cfg.MaxEntryBytes)
		}
		if entry.Text == "" && entry.Carrier == nil {
			continue
		}
		size := entryPayloadBytes(entry)
		if cfg.MaxBytes > 0 && used+size > cfg.MaxBytes {
			remaining := cfg.MaxBytes - used
			if remaining <= 0 {
				continue
			}
			if entry.Carrier == nil {
				entry.Text = truncateEntryText(entry.Text, entry.Untrusted, remaining)
			} else {
				// Do not cut a structured payload into malformed JSON. A carrier
				// is retained only when it fits as a complete bounded record.
				continue
			}
			size = entryPayloadBytes(entry)
			if size == 0 || used+size > cfg.MaxBytes {
				continue
			}
		}
		out = append(out, entry)
		used += size
	}
	return out
}

func entryPayloadBytes(e Entry) int {
	n := len(e.Text)
	if e.Carrier != nil {
		n += len(e.Carrier.Type) + len(e.Carrier.Payload)
	}
	return n
}

func entriesPayloadBytes(entries []Entry) int {
	n := 0
	for _, e := range entries {
		n += entryPayloadBytes(e)
	}
	return n
}

func cloneEntries(in []Entry) []Entry {
	if len(in) == 0 {
		return nil
	}
	out := make([]Entry, len(in))
	for i, e := range in {
		out[i] = e
		if e.Carrier != nil {
			c := *e.Carrier
			out[i].Carrier = &c
		}
	}
	return out
}

func entryKey(e Entry) string {
	return fmt.Sprintf("%d/%d/%s/%s", e.ItemIndex, e.priority, e.Kind, e.ItemID)
}
