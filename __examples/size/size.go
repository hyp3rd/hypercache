package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/backend"
	"github.com/hyp3rd/hypercache/internal/constants"
)

const (
	numUsers      = 100
	maxCacheSize  = 37326
	cacheCapacity = 100000
)

type user struct {
	Name       string
	Age        int
	LastAccess time.Time
	Pass       string
	Email      string
	Phone      string
	IsAdmin    bool
	IsGuest    bool
	IsBanned   bool
}

func generateRandomUsers() []user {
	users := make([]user, 0, numUsers)

	for i := range numUsers {
		users = append(users, user{
			Name:       fmt.Sprintf("User%d", i),
			Age:        rand.IntN(numUsers),
			LastAccess: time.Now(),
			Pass:       fmt.Sprintf("Pass%d", i),
			Email:      fmt.Sprintf("user%d@example.com", i),
			Phone:      fmt.Sprintf("123456789%d", i),
			IsAdmin:    rand.IntN(2) == 0,
			IsGuest:    rand.IntN(2) == 0,
			IsBanned:   rand.IntN(2) == 0,
		})
	}

	return users
}

func main() {
	config := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)

	config.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](0),
		hypercache.WithEvictionAlgorithm[backend.InMemory]("cawolfu"),
		hypercache.WithMaxCacheSize[backend.InMemory](maxCacheSize),
	}

	config.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](cacheCapacity),
	}

	// Create a new HyperCache with a capacity of 10
	cache, err := hypercache.New(hypercache.GetDefaultManager(), config)
	if err != nil {
		panic(err)
	}

	for i := range 3 {
		err = cache.Set(context.TODO(), fmt.Sprintf("key-%d", i), generateRandomUsers(), 0)
		if err != nil {
			fmt.Fprintln(os.Stdout, err, "set", i)
		}
	}

	key, ok := cache.GetWithInfo(context.TODO(), "key-1")

	if ok {
		fmt.Fprintln(os.Stdout, "value", key.Value)
		fmt.Fprintln(os.Stdout, "size", key.Size)
	} else {
		fmt.Fprintln(os.Stdout, "key not found")
	}

	fmt.Fprintln(os.Stdout, cache.Count(context.TODO()))
	fmt.Fprintln(os.Stdout, cache.Allocation())
}
