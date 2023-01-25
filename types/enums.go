package types

// SortingField is a type that represents the field to sort the cache items by.
type SortingField string

// Constants for the different fields that the cache items can be sorted by.
const (
	SortByKey         SortingField = "Key"         // Sort by the key of the cache item
	SortBySize        SortingField = "Size"        // Sort by the size in bytes of the cache item
	SortByLastAccess  SortingField = "LastAccess"  // Sort by the last access time of the cache item
	SortByAccessCount SortingField = "AccessCount" // Sort by the number of times the cache item has been accessed
	SortByExpiration  SortingField = "Expiration"  // Sort by the expiration duration of the cache item
)

// String returns the string representation of the SortingField.
func (f SortingField) String() string {
	return string(f)
}

// Stat is a type that represents a different stat values that can be collected by the stats collector.
type Stat string

const (
	// StatIncr represent a stat that should be incremented
	StatIncr Stat = "incr"
	// StatDecr represent a stat that should be decremented
	StatDecr Stat = "decr"
	// StatTiming represent a stat that represents the time it takes for an event to occur
	StatTiming Stat = "timing"
	// StatGauge represent a stat that represents the current value of a statistic
	StatGauge Stat = "gauge"
	// StatHistogram represent a stat that represents the statistical distribution of a set of values
	StatHistogram Stat = "histogram"
)

// String returns the string representation of a Stat.
func (s Stat) String() string {
	return string(s)
}
