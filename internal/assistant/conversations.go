package assistant

import (
	"cmp"
	"context"
	"crypto/rand"
	"log/slog"
	"sync"
	"time"
)

// chat is one conversation about one budget.
type chat struct {
	id           string
	budget       string
	conversation Conversation
	tools        *mcpTools
	lastUsed     time.Time
	busy         sync.Mutex
}

// getOrCreateChat returns the chat id when it is about the budget, and a
// new chat otherwise.
func (a *Assistant) getOrCreateChat(ctx context.Context, id, budget string) (*chat, error) {
	a.removeExpiredChats()
	if existing := a.getChat(id, budget); existing != nil {
		return existing, nil
	}
	return a.createChat(ctx, budget)
}

func (a *Assistant) getChat(id, budget string) *chat {
	now := a.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	existing, found := a.chats[id]
	if !found || existing.budget != budget {
		return nil
	}
	existing.lastUsed = now
	return existing
}

// removeExpiredChats closes and removes the chats unused for longer than
// the TTL.
func (a *Assistant) removeExpiredChats() {
	now := a.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, c := range a.chats {
		if now.Sub(c.lastUsed) > a.TTL {
			c.tools.close()
			delete(a.chats, id)
		}
	}
}

func (a *Assistant) createChat(ctx context.Context, budget string) (*chat, error) {
	if _, err := a.App.Get(ctx, budget); err != nil {
		return nil, err
	}
	provider, err := a.Provider()
	if err != nil {
		return nil, err
	}
	tools, err := connectTools(ctx, a.App, budget, cmp.Or(a.Log, slog.Default()))
	if err != nil {
		return nil, err
	}
	system := systemPrompt(budget, tools.instructions)
	created := &chat{
		id:           rand.Text(),
		budget:       budget,
		conversation: provider.Start(system, tools.tools),
		tools:        tools,
		lastUsed:     a.Now(),
	}
	a.mu.Lock()
	a.chats[created.id] = created
	a.mu.Unlock()
	return created, nil
}
