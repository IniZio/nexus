package store

type SpotlightRepository interface {
	UpsertSpotlightForwardRow(row SpotlightForwardRow) error
	DeleteSpotlightForwardRow(id string) error
	ListSpotlightForwardRows() ([]SpotlightForwardRow, error)
}
