package app

import (
	"context"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// startNotifications prepares the desktop's notifications, and a desktop that
// has none is not an error. Deliberately not registered as a service of the
// application: a service whose startup fails stops the application from
// starting, and a session bus that is not there, or an unsigned macOS bundle,
// must cost the toasts and nothing else.
func (a *App) startNotifications(ctx context.Context) {
	if a.wails == nil {
		return
	}
	n := notifications.New()
	if err := n.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		log.Printf("native notifications are not available: %v", err)
		return
	}
	a.notifier = n
}

// NotificationsAvailable says whether SendNotification can show anything.
func (a *App) NotificationsAvailable() bool {
	return a.notifier != nil
}

// SendNotification shows a native notification. The renderer cleans the text
// before it gets here, since part of it comes from the network: see
// utils/notifyText.js.
func (a *App) SendNotification(id, title, subtitle, body string) error {
	if a.notifier == nil {
		return nil
	}
	return a.notifier.SendNotification(notifications.NotificationOptions{
		ID:       id,
		Title:    title,
		Subtitle: subtitle,
		Body:     body,
	})
}
