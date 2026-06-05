package wecom

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectDispatch is a test helper that records dispatched pendingMsg values.
type collectDispatch struct {
	mu       sync.Mutex
	messages []*pendingMsg
	ch       chan *pendingMsg
}

func newCollectDispatch() *collectDispatch {
	return &collectDispatch{ch: make(chan *pendingMsg, 10)}
}

func (c *collectDispatch) fn(_ context.Context, pm *pendingMsg) {
	c.mu.Lock()
	c.messages = append(c.messages, pm)
	c.mu.Unlock()
	c.ch <- pm
}

func (c *collectDispatch) waitN(
	t *testing.T,
	n int,
	timeout time.Duration,
) []*pendingMsg {
	t.Helper()
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		got := len(c.messages)
		c.mu.Unlock()
		if got >= n {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.messages[:n]
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d dispatches, got %d", n, got)
		case <-c.ch:
		}
	}
}

func TestCoalescer_SingleMessage(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(100*time.Millisecond, col.fn)

	c.add(context.Background(), 1, 100, "alice", "single", "req-1",
		"hello", nil, nil)

	msgs := col.waitN(t, 1, time.Second)
	assert.Equal(t, "hello", msgs[0].mergedText())
	assert.Equal(t, "req-1", msgs[0].reqID)
	assert.Equal(t, int64(1), msgs[0].hashedChat)
	assert.Equal(t, "alice", msgs[0].senderName)
}

func TestCoalescer_MergesSameSender(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(200*time.Millisecond, col.fn)

	c.add(context.Background(), 1, 100, "alice", "single", "req-1",
		"看看这个 pdf", nil, nil)
	time.Sleep(50 * time.Millisecond)
	c.add(context.Background(), 1, 100, "alice", "single", "req-2",
		"[file]", []string{"https://example.com/doc.pdf"},
		[]string{"aeskey1"})

	msgs := col.waitN(t, 1, time.Second)
	require.Len(t, msgs, 1)
	assert.Equal(t, "看看这个 pdf\n[file]", msgs[0].mergedText())
	assert.Equal(t, "req-2", msgs[0].reqID)
	assert.Equal(t, []string{"https://example.com/doc.pdf"}, msgs[0].mediaURLs)
	assert.Equal(t, []string{"aeskey1"}, msgs[0].aesKeys)
}

func TestCoalescer_DifferentChatsIndependent(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(100*time.Millisecond, col.fn)

	c.add(context.Background(), 1, 100, "alice", "single", "req-1",
		"chat1 msg", nil, nil)
	c.add(context.Background(), 2, 200, "bob", "group", "req-2",
		"chat2 msg", nil, nil)

	msgs := col.waitN(t, 2, time.Second)

	chats := map[int64]string{}
	for _, m := range msgs {
		chats[m.hashedChat] = m.mergedText()
	}
	assert.Equal(t, "chat1 msg", chats[1])
	assert.Equal(t, "chat2 msg", chats[2])
}

func TestCoalescer_DifferentSendersInSameChatIndependent(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(100*time.Millisecond, col.fn)

	c.add(context.Background(), 1, 100, "alice", "group", "req-1",
		"first", nil, nil)
	c.add(context.Background(), 1, 200, "bob", "group", "req-2",
		"second", nil, nil)

	msgs := col.waitN(t, 2, time.Second)
	require.Len(t, msgs, 2)
	texts := map[int64]string{}
	for _, m := range msgs {
		texts[m.hashedSender] = m.mergedText()
	}
	assert.Equal(t, "first", texts[100])
	assert.Equal(t, "second", texts[200])
}

func TestCoalescer_TextAndFilesMerged(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(200*time.Millisecond, col.fn)

	c.add(context.Background(), 42, 100, "alice", "single", "req-1",
		"我修了一下，你再看看", nil, nil)
	time.Sleep(30 * time.Millisecond)
	c.add(context.Background(), 42, 100, "alice", "single", "req-2",
		"[file]", []string{"https://example.com/fix.pdf"},
		[]string{"key123"})

	msgs := col.waitN(t, 1, time.Second)
	assert.Equal(t, "我修了一下，你再看看\n[file]", msgs[0].mergedText())
	assert.Equal(t, []string{"https://example.com/fix.pdf"}, msgs[0].mediaURLs)
	assert.Equal(t, []string{"key123"}, msgs[0].aesKeys)
	assert.Equal(t, "req-2", msgs[0].reqID)
}

func TestCoalescer_EmptyTextNotAppended(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(100*time.Millisecond, col.fn)

	c.add(context.Background(), 1, 100, "alice", "single", "req-1",
		"hello", nil, nil)
	time.Sleep(20 * time.Millisecond)
	c.add(context.Background(), 1, 100, "alice", "single", "req-2",
		"", []string{"url"}, []string{"key"})

	msgs := col.waitN(t, 1, time.Second)
	assert.Equal(t, "hello", msgs[0].mergedText())
	assert.Equal(t, []string{"url"}, msgs[0].mediaURLs)
}

func TestCoalescer_TimerResetOnMerge(t *testing.T) {
	col := newCollectDispatch()
	c := newChatCoalescer(150*time.Millisecond, col.fn)

	c.add(context.Background(), 1, 100, "alice", "single", "req-1",
		"first", nil, nil)
	time.Sleep(100 * time.Millisecond)
	c.add(context.Background(), 1, 100, "alice", "single", "req-2",
		"second", nil, nil)

	col.mu.Lock()
	assert.Empty(t, col.messages)
	col.mu.Unlock()

	msgs := col.waitN(t, 1, time.Second)
	assert.Equal(t, "first\nsecond", msgs[0].mergedText())
}
