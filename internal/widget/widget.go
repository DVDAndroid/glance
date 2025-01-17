package widget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/glanceapp/glance/internal/feed"

	"gopkg.in/yaml.v3"
)

var uniqueID atomic.Uint64

func New(widgetType string) (Widget, error) {
	var widget Widget

	switch widgetType {
	case "calendar":
		widget = &Calendar{}
	case "clock":
		widget = &Clock{}
	case "weather":
		widget = &Weather{}
	case "bookmarks":
		widget = &Bookmarks{}
	case "iframe":
		widget = &IFrame{}
	case "html":
		widget = &HTML{}
	case "hacker-news":
		widget = &HackerNews{}
	case "releases":
		widget = &Releases{}
	case "videos":
		widget = &Videos{}
	case "markets", "stocks":
		widget = &Markets{}
	case "reddit":
		widget = &Reddit{}
	case "rss":
		widget = &RSS{}
	case "monitor":
		widget = &Monitor{}
	case "twitch-top-games":
		widget = &TwitchGames{}
	case "twitch-channels":
		widget = &TwitchChannels{}
	case "lobsters":
		widget = &Lobsters{}
	case "change-detection":
		widget = &ChangeDetection{}
	case "repository":
		widget = &Repository{}
	case "search":
		widget = &Search{}
	case "extension":
		widget = &Extension{}
	case "group":
		widget = &Group{}
	default:
		return nil, fmt.Errorf("unknown widget type: %s", widgetType)
	}

	widget.SetID(uniqueID.Add(1))

	return widget, nil
}

type Widgets []Widget

func (w *Widgets) UnmarshalYAML(node *yaml.Node) error {
	var nodes []yaml.Node

	if err := node.Decode(&nodes); err != nil {
		return err
	}

	for _, node := range nodes {
		meta := struct {
			Type string `yaml:"type"`
		}{}

		if err := node.Decode(&meta); err != nil {
			return err
		}

		widget, err := New(meta.Type)

		if err != nil {
			return err
		}

		if err = node.Decode(widget); err != nil {
			return err
		}

		*w = append(*w, widget)
	}

	return nil
}

type Widget interface {
	Initialize() error
	RequiresUpdate(*time.Time) bool
	SetProviders(*Providers)
	Update(context.Context)
	Render() template.HTML
	GetType() string
	GetID() uint64
	SetID(uint64)
	HandleRequest(w http.ResponseWriter, r *http.Request)
	SetHideHeader(bool)
}

type cacheType int

const (
	cacheTypeInfinite cacheType = iota
	cacheTypeDuration
	cacheTypeOnTheHour
)

type widgetBase struct {
	ID                  uint64        `yaml:"-"`
	Providers           *Providers    `yaml:"-"`
	Type                string        `yaml:"type"`
	Title               string        `yaml:"title"`
	TitleURL            string        `yaml:"title-url"`
	CSSClass            string        `yaml:"css-class"`
	CustomCacheDuration DurationField `yaml:"cache"`
	ContentAvailable    bool          `yaml:"-"`
	Error               error         `yaml:"-"`
	Notice              error         `yaml:"-"`
	templateBuffer      bytes.Buffer  `yaml:"-"`
	cacheDuration       time.Duration `yaml:"-"`
	cacheType           cacheType     `yaml:"-"`
	nextUpdate          time.Time     `yaml:"-"`
	updateRetriedTimes  int           `yaml:"-"`
	HideHeader          bool          `yaml:"-"`
}

type Providers struct {
	AssetResolver func(string) string
}

func (w *widgetBase) RequiresUpdate(now *time.Time) bool {
	if w.cacheType == cacheTypeInfinite {
		return false
	}

	if w.nextUpdate.IsZero() {
		return true
	}

	return now.After(w.nextUpdate)
}

func (w *widgetBase) Update(ctx context.Context) {

}

func (w *widgetBase) GetID() uint64 {
	return w.ID
}

func (w *widgetBase) SetID(id uint64) {
	w.ID = id
}

func (w *widgetBase) SetHideHeader(value bool) {
	w.HideHeader = value
}

func (widget *widgetBase) HandleRequest(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (w *widgetBase) GetType() string {
	return w.Type
}

func (w *widgetBase) SetProviders(providers *Providers) {
	w.Providers = providers
}

func (w *widgetBase) render(data any, t *template.Template) template.HTML {
	w.templateBuffer.Reset()
	err := t.Execute(&w.templateBuffer, data)

	if err != nil {
		w.ContentAvailable = false
		w.Error = err

		slog.Error("failed to render template", "error", err)

		// need to immediately re-render with the error,
		// otherwise risk breaking the page since the widget
		// will likely be partially rendered with tags not closed.
		w.templateBuffer.Reset()
		err2 := t.Execute(&w.templateBuffer, data)

		if err2 != nil {
			slog.Error("failed to render error within widget", "error", err2, "initial_error", err)
			w.templateBuffer.Reset()
			// TODO: add some kind of a generic widget error template when the widget
			// failed to render, and we also failed to re-render the widget with the error
		}
	}

	return template.HTML(w.templateBuffer.String())
}

func (w *widgetBase) withTitle(title string) *widgetBase {
	if w.Title == "" {
		w.Title = title
	}

	return w
}

func (w *widgetBase) withTitleURL(titleURL string) *widgetBase {
	if w.TitleURL == "" {
		w.TitleURL = titleURL
	}

	return w
}

func (w *widgetBase) withCacheDuration(duration time.Duration) *widgetBase {
	w.cacheType = cacheTypeDuration

	if duration == -1 || w.CustomCacheDuration == 0 {
		w.cacheDuration = duration
	} else {
		w.cacheDuration = time.Duration(w.CustomCacheDuration)
	}

	return w
}

func (w *widgetBase) withCacheOnTheHour() *widgetBase {
	w.cacheType = cacheTypeOnTheHour

	return w
}

func (w *widgetBase) withNotice(err error) *widgetBase {
	w.Notice = err

	return w
}

func (w *widgetBase) withError(err error) *widgetBase {
	if err == nil && !w.ContentAvailable {
		w.ContentAvailable = true
	}

	w.Error = err

	return w
}

func (w *widgetBase) canContinueUpdateAfterHandlingErr(err error) bool {
	// TODO: needs covering more edge cases.
	// if there's partial content and we update early there's a chance
	// the early update returns even less content than the initial update.
	// need some kind of mechanism that tells us whether we should update early
	// or not depending on the number of things that failed during the initial
	// and subsequent update and how they failed - ie whether it was server
	// error (like gateway timeout, do retry early) or client error (like
	// hitting a rate limit, don't retry early). will require reworking a
	// good amount of code in the feed package and probably having a custom
	// error type that holds more information because screw wrapping errors.
	// alternatively have a resource cache and only refetch the failed resources,
	// then rebuild the widget.

	if err != nil {
		w.scheduleEarlyUpdate()

		if !errors.Is(err, feed.ErrPartialContent) {
			w.withError(err)
			w.withNotice(nil)
			return false
		}

		w.withError(nil)
		w.withNotice(err)
		return true
	}

	w.withNotice(nil)
	w.withError(nil)
	w.scheduleNextUpdate()
	return true
}

func (w *widgetBase) getNextUpdateTime() time.Time {
	now := time.Now()

	if w.cacheType == cacheTypeDuration {
		return now.Add(w.cacheDuration)
	}

	if w.cacheType == cacheTypeOnTheHour {
		return now.Add(time.Duration(
			((60-now.Minute())*60)-now.Second(),
		) * time.Second)
	}

	return time.Time{}
}

func (w *widgetBase) scheduleNextUpdate() *widgetBase {
	w.nextUpdate = w.getNextUpdateTime()
	w.updateRetriedTimes = 0

	return w
}

func (w *widgetBase) scheduleEarlyUpdate() *widgetBase {
	w.updateRetriedTimes++

	if w.updateRetriedTimes > 5 {
		w.updateRetriedTimes = 5
	}

	nextEarlyUpdate := time.Now().Add(time.Duration(math.Pow(float64(w.updateRetriedTimes), 2)) * time.Minute)
	nextUsualUpdate := w.getNextUpdateTime()

	if nextEarlyUpdate.After(nextUsualUpdate) {
		w.nextUpdate = nextUsualUpdate
	} else {
		w.nextUpdate = nextEarlyUpdate
	}

	return w
}
