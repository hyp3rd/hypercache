package stats

// Stats is a map that stores statistical data.
// The keys are the stat names and the values are the stat values.
type Stats map[string]*Stat

// Stat represents statistical data for a specific stat.
type Stat struct {
	// Mean value of the stat.
	Mean float64
	// Median value of the stat.
	Median float64
	// Min is the minimum value of the stat.
	Min int64
	// Max maximum value of the stat.
	Max int64
	// Values slice of all the values of the stat.
	Values []int64
	// Count the number of values of the stat.
	Count int
	// Sum of all the values of the stat.
	Sum int64
	// Variance of the values of the stat.
	Variance float64
}
