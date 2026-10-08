package locales

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var files embed.FS

var supported = []string{"ru-RU", "en-US"}
var catalogs sync.Map

func Supported() []string { return append([]string(nil), supported...) }

func Catalog(locale string) map[string]string {
	locale = Normalize(locale)
	if cached, ok := catalogs.Load(locale); ok {
		return cached.(map[string]string)
	}
	data, err := files.ReadFile(locale + ".yaml")
	if err != nil {
		panic(err)
	}
	result := make(map[string]string)
	if err := yaml.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	catalogs.Store(locale, result)
	return result
}

func T(locale, key string, args ...any) string {
	value := Catalog(locale)[key]
	if value == "" {
		value = Catalog("en-US")[key]
	}
	if value == "" {
		value = key
	}
	if len(args) > 0 {
		return fmt.Sprintf(value, args...)
	}
	return value
}

func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ",")[0]))
	if strings.HasPrefix(value, "en") {
		return "en-US"
	}
	return "ru-RU"
}
