package policy

// Invalidators applies every change to several caches, such as the resolver
// and the API key cache, so that one subscription keeps them all fresh.
type Invalidators []Invalidator

// Invalidate implements Invalidator.
func (is Invalidators) Invalidate(c Change) {
	for _, i := range is {
		i.Invalidate(c)
	}
}

// Flush implements Invalidator.
func (is Invalidators) Flush() {
	for _, i := range is {
		i.Flush()
	}
}

// ClientCache holds per-client data that a client change makes stale, such
// as the client's API keys. *client.KeyCache satisfies it.
type ClientCache interface {
	InvalidateClient(clientID string)
	Flush()
}

// ClientChanges adapts a ClientCache to an Invalidator: it acts on client
// changes, ignores policy changes, and flushes along with everything else.
func ClientChanges(c ClientCache) Invalidator {
	return clientChanges{c}
}

type clientChanges struct {
	ClientCache
}

func (c clientChanges) Invalidate(ch Change) {
	if ch.Kind == KindClient {
		c.InvalidateClient(ch.ID)
	}
}
