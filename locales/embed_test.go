package locales

import "testing"

func TestCatalogsHaveSameKeys(t *testing.T) {
	ru, en := Catalog("ru-RU"), Catalog("en-US")
	for key := range ru {
		if en[key] == "" {
			t.Errorf("en-US is missing %q", key)
		}
	}
	for key := range en {
		if ru[key] == "" {
			t.Errorf("ru-RU is missing %q", key)
		}
	}
}
