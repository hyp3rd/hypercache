package backend

import (
	"context"

	"github.com/redis/go-redis/v9"

	"github.com/hyp3rd/ewrap"

	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/internal/libs/serializer"
	"github.com/hyp3rd/hypercache/pkg/cache"
)

// redisCmd abstracts the subset of go-redis client API we need.
type redisCmd interface {
	TxPipeline() redis.Pipeliner
	Pipelined(ctx context.Context, fn func(redis.Pipeliner) error) ([]redis.Cmder, error)
	SMembers(ctx context.Context, key string) *redis.StringSliceCmd
}

func redisSet(ctx context.Context, client redisCmd, keysSetName string, item *cache.Item, ser serializer.ISerializer) error {
	pipe := client.TxPipeline()

	// Validate item
	err := item.Valid()
	if err != nil {
		return err
	}

	// Serialize item
	data, err := ser.Marshal(item)
	if err != nil {
		return err
	}

	expiration := item.Expiration.String()

	err = pipe.HSet(
		ctx,
		item.Key,
		map[string]any{
			"data":       data,
			"expiration": expiration,
		},
	).Err()
	if err != nil {
		return ewrap.Wrap(err, "failed to set item in redis")
	}

	// Track key and TTL
	pipe.SAdd(ctx, keysSetName, item.Key)

	if item.Expiration > 0 {
		pipe.Expire(ctx, item.Key, item.Expiration)
	}

	_, err = pipe.Exec(ctx)
	if err != nil {
		return ewrap.Wrap(err, "failed to execute redis pipeline")
	}

	// success

	return nil
}

func redisList(ctx context.Context, client redisCmd, keysSetName string, ser serializer.ISerializer, pool *cache.ItemPoolManager, filters ...IFilter) ([]*cache.Item, error) {
	// Get keys in the logical set
	keys, err := client.SMembers(ctx, keysSetName).Result()
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to get keys from redis")
	}

	// Pipeline fetches
	cmds, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, key := range keys {
			pipe.HGetAll(ctx, key)
		}

		return nil
	})
	if err != nil {
		return nil, ewrap.Wrap(err, "failed to execute redis pipeline while listing")
	}

	items := make([]*cache.Item, 0, len(keys))

	for _, cmd := range cmds {
		command, ok := cmd.(*redis.MapStringStringCmd)
		if !ok {
			continue
		}

		data, err := command.Result()
		if err != nil {
			return nil, ewrap.Wrap(err, "failed to get item data from redis")
		}

		item := pool.Get()
		defer pool.Put(item)

		err = ser.Unmarshal([]byte(data["data"]), item)
		if err == nil {
			items = append(items, item)
		}
	}

	if len(filters) > 0 {
		for _, filter := range filters {
			items, err = filter.ApplyFilter(constants.RedisBackend, items)
		}
	}

	return items, err
}

func redisRemove(ctx context.Context, client redisCmd, keysSetName string, keys ...string) error {
	pipe := client.TxPipeline()

	_, err := pipe.SRem(ctx, keysSetName, keys).Result()
	if err != nil {
		return ewrap.Wrap(err, "removing keys from set")
	}

	_, err = pipe.Del(ctx, keys...).Result()
	if err != nil {
		return ewrap.Wrap(err, "removing keys")
	}

	_, err = pipe.Exec(ctx)

	return ewrap.Wrap(err, "executing pipeline")
}
