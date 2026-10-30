package providerprofiles

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed catalog.json
var embeddedCatalog []byte

var (
	embeddedCatalogOnce sync.Once
	embeddedCatalogRes  *Catalog
	errEmbeddedCatalog  error
)

// EmbeddedCatalog loads the checked-in catalog without network or process work.
func EmbeddedCatalog() (*Catalog, error) {
	embeddedCatalogOnce.Do(func() {
		var profiles []Profile
		if err := json.Unmarshal(embeddedCatalog, &profiles); err != nil {
			errEmbeddedCatalog = fmt.Errorf("provider profiles: decode embedded catalog: %w", err)
			return
		}
		embeddedCatalogRes, errEmbeddedCatalog = NewCatalog(profiles)
	})
	return embeddedCatalogRes, errEmbeddedCatalog
}

func EmbeddedProfile(id string) (Profile, error) {
	catalog, err := EmbeddedCatalog()
	if err != nil {
		return Profile{}, err
	}
	for _, profile := range catalog.Profiles() {
		if profile.ID == id {
			return profile, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown embedded provider profile %q", id)
}
