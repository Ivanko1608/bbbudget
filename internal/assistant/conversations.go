package assistant

import (
	"context"
	"crypto/rand"
	"sync"
	"time"
)

// chat is one conversation about one budget.
type chat struct {
	id           string
	budget       string
	conversation Conversation
	tools        *toolbox
	lastUsed     time.Time
	busy         sync.Mutex
}

// chatFor continues the conversation id when it is about the budget, and
// starts a new one otherwise.
func (a *Assistant) chatFor(ctx context.Context, id, budget string) (*chat, error) {
	if earlier := a.resume(id, budget); earlier != nil {
		return earlier, nil
	}
	return a.start(ctx, budget)
}

func (a *Assistant) resume(id, budget string) *chat {
	now := a.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forgetIdle(now)
	earlier, found := a.chats[id]
	if !found || earlier.budget != budget {
		return nil
	}
	earlier.lastUsed = now
	return earlier
}

// forgetIdle drops the conversations idle for longer than the TTL. The
// caller holds a.mu.
func (a *Assistant) forgetIdle(now time.Time) {
	for id, idle := range a.chats {
		if now.Sub(idle.lastUsed) > a.TTL {
			idle.tools.close()
			delete(a.chats, id)
		}
	}
}

func (a *Assistant) start(ctx context.Context, budget string) (*chat, error) {
	if _, err := a.App.Get(ctx, budget); err != nil {
		return nil, err
	}
	provider, err := a.Provider()
	if err != nil {
		return nil, err
	}
	tools, err := connectTools(ctx, a.App, budget)
	if err != nil {
		return nil, err
	}
	system := systemPrompt(budget, tools.instructions)
	started := &chat{
		id:           rand.Text(),
		budget:       budget,
		conversation: provider.Start(system, tools.tools),
		tools:        tools,
		lastUsed:     a.Now(),
	}
	a.mu.Lock()
	a.chats[started.id] = started
	a.mu.Unlock()
	return started, nil
}
