package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Bot holds the Discord session and the set of channel IDs to monitor.
type Bot struct {
	session    *discordgo.Session
	channelIDs map[string]bool
	hub        *Hub
}

// NewBot creates and opens a Discord bot session.
func NewBot(token string, channelIDs []string, hub *Hub) (*Bot, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("creating discord session: %w", err)
	}

	// IntentsGuilds      → receive ChannelPinsUpdate events
	// IntentsGuildMessages → receive MessageCreate events
	// IntentMessageContent → read message body text (privileged, must be enabled in Dev Portal)
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentMessageContent

	idSet := make(map[string]bool, len(channelIDs))
	for _, id := range channelIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			idSet[id] = true
		}
	}

	bot := &Bot{
		session:    session,
		channelIDs: idSet,
		hub:        hub,
	}

	session.AddHandler(bot.onMessageCreate)

	if err := session.Open(); err != nil {
		return nil, fmt.Errorf("opening discord session: %w", err)
	}

	return bot, nil
}

// PrefillBuffer fetches the most recent messages from each configured channel
// and sends them to the hub so new viewers see recent history immediately.
func (b *Bot) PrefillBuffer(limit int) {
	for channelID := range b.channelIDs {
		messages, err := b.session.ChannelMessages(channelID, limit, "", "", "")
		if err != nil {
			log.Printf("prefill: failed to fetch messages from channel %s: %v", channelID, err)
			continue
		}

		// Discord returns messages newest-first; reverse to get chronological order.
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}

		for _, m := range messages {
			if m.Author == nil || m.Author.Bot {
				continue
			}
			b.hub.broadcast <- discordMessageToMessage(m)
		}
	}
}

// FetchPinnedMessages returns the current pinned messages from all configured
// channels, in pin order (most recently pinned first).
//
// Unlike the live-chat feed, bot/webhook messages are intentionally included:
// race organizers often pin bot-posted task summaries or automated announcements.
func (b *Bot) FetchPinnedMessages() ([]Message, error) {
	var all []Message
	for channelID := range b.channelIDs {
		msgs, err := b.session.ChannelMessagesPinned(channelID)
		if err != nil {
			log.Printf("pinned: failed to fetch from channel %s: %v", channelID, err)
			continue
		}
		for _, m := range msgs {
			if m == nil {
				continue
			}
			all = append(all, discordMessageToMessage(m))
		}
	}
	return all, nil
}

// ChannelURL returns the Discord deep-link for the first configured channel.
// It resolves the guild ID via the API so no extra config is needed.
func (b *Bot) ChannelURL() string {
	for channelID := range b.channelIDs {
		ch, err := b.session.Channel(channelID)
		if err == nil && ch.GuildID != "" {
			return fmt.Sprintf("https://discord.com/channels/%s/%s", ch.GuildID, channelID)
		}
	}
	return "https://discord.com"
}

// FetchPinnedCount returns the number of currently pinned messages in a channel.
func (b *Bot) FetchPinnedCount(channelID string) int {
	msgs, err := b.session.ChannelMessagesPinned(channelID)
	if err != nil {
		return 0
	}
	return len(msgs)
}

// Close cleanly shuts down the Discord session.
func (b *Bot) Close() {
	b.session.Close()
}

func (b *Bot) onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot {
		return
	}
	if !b.channelIDs[m.ChannelID] {
		return
	}
	b.hub.broadcast <- discordMessageToMessage(m.Message)
}

func discordMessageToMessage(m *discordgo.Message) Message {
	var author, avatar string
	if m.Author != nil {
		author = m.Author.Username
		avatar = m.Author.AvatarURL("64")
	} else if m.WebhookID != "" {
		author = "Announcement"
	}

	content := m.Content
	// Fall back to the first embed description when the message body is empty
	// (common for bot/webhook messages that use rich embeds).
	if content == "" && len(m.Embeds) > 0 && m.Embeds[0].Description != "" {
		content = m.Embeds[0].Description
	}

	return Message{
		Author:    author,
		Avatar:    avatar,
		Content:   content,
		Timestamp: m.Timestamp,
	}
}
