package types

// SortingField is a type that represents the field to sort the cache items by.
type SortingField string

// Constants for the different fields that the cache items can be sorted by.
const (
	SortByKey         SortingField = "Key"         // Sort by the key of the cache item
	SortByValue       SortingField = "Value"       // Sort by the value of the cache item
	SortByLastAccess  SortingField = "LastAccess"  // Sort by the last access time of the cache item
	SortByAccessCount SortingField = "AccessCount" // Sort by the number of times the cache item has been accessed
	SortByExpiration  SortingField = "Expiration"  // Sort by the expiration duration of the cache item
)

// String returns the string representation of the SortingField.
func (f SortingField) String() string {
	return string(f)
}
