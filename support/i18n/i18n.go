package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/prismgo/framework/translation"
	"golang.org/x/text/language"
)

//go:embed lang/*.json
var files embed.FS

type localeKey struct{}

func Register() error {
	data, err := files.ReadFile("lang/en.json")
	if err != nil {
		return err
	}
	var lines map[string]any
	if err := json.Unmarshal(data, &lines); err != nil {
		return err
	}
	entries := make(map[string]any, len(lines))
	for key, value := range lines {
		entries["messages."+key] = value
	}
	translation.AddLines(entries, "en", "admin")
	return nil
}

func Match(header string) string {
	locales := []string{"zh", "en"}
	base, _ := language.Make(translation.Locale()).Base()
	if base.String() == "en" {
		locales[0], locales[1] = locales[1], locales[0]
	}
	_, index := language.MatchStrings(language.NewMatcher([]language.Tag{language.Make(locales[0]), language.Make(locales[1])}), header)
	return locales[index]
}

func WithLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, localeKey{}, locale)
}

func Locale(ctx context.Context) string {
	if locale, ok := ctx.Value(localeKey{}).(string); ok {
		return locale
	}
	return Match("")
}

func Text(locale, key string, args ...any) string {
	if locale == "" {
		locale = Match("")
	}
	text := key
	if locale != "zh" {
		text = strings.TrimPrefix(translation.Get("admin::messages."+key, nil, locale), "admin::messages.")
	}
	if len(args) > 0 {
		values := slices.Clone(args)
		for i, value := range values {
			if err, ok := value.(error); ok {
				values[i] = Localize(err, locale)
			}
		}
		return fmt.Errorf(text, values...).Error()
	}
	return text
}

// Error保留原始key和参数，直到HTTP、CLI或WS出口才决定语言。
type Error struct {
	Key  string
	Args []any
}

func (e Error) Error() string { return e.Key + " " + fmt.Sprint(e.Args...) }
func (e Error) Unwrap() error {
	for _, value := range e.Args {
		if err, ok := value.(error); ok {
			return err
		}
	}
	return nil
}
func Errorf(key string, args ...any) error { return Error{Key: key, Args: args} }

func Message(err error, locale string) string {
	var message Error
	if errors.As(err, &message) {
		return Text(locale, message.Key, message.Args...)
	}
	return Text(locale, err.Error())
}

type localizedError struct {
	error
	text string
}

func (e localizedError) Error() string { return e.text }
func (e localizedError) Unwrap() error { return e.error }
func Localize(err error, locale string) error {
	if err == nil {
		return nil
	}
	return localizedError{error: err, text: Message(err, locale)}
}
