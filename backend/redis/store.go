package redis

import (
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

type Store struct {
	Client *redis.Client
}

// New
// @param opt
func New(opt *redis.Options) *Store {
	cli := redis.NewClient(opt)

	return &Store{Client: cli}
}

func NewStore(opts ...Option) (*Store, error) {
	// Setup redis client
	opt := &redis.Options{
		MaxRetries:   10,
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolFIFO:     false,
		PoolSize:     10,
	}

	ApplyOptions(opt, opts...)

	if opt.Addr == "" {
		return nil, fmt.Errorf("redis address is empty")
	}

	cli := redis.NewClient(opt)

	return &Store{Client: cli}, nil
}

// NewWithDb
// @param tx
func NewWithDb(tx *redis.Client) *Store {
	return &Store{Client: tx}
}

// func NewOptions() (opt *redis.Options, err error) {

// 	// read options from config
// 	opt = &redis.Options{
// 		Addr:         config.Redis.Address,
// 		Password:     config.Redis.Password,
// 		DB:           config.Redis.DB,
// 		MaxRetries:   10,
// 		DialTimeout:  10 * time.Second,
// 		ReadTimeout:  30 * time.Second,
// 		WriteTimeout: 30 * time.Second,
// 		PoolFIFO:     false,
// 		PoolSize:     10,
// 	}

// 	// read CA cert
// 	caPath, ok := os.LookupEnv("TLS_CA_CERT_FILE")
// 	if ok && caPath != "" {
// 		// ref https://pkg.go.dev/crypto/tls#example-Dial
// 		rootCertPool := x509.NewCertPool()
// 		pem, err := ioutil.ReadFile(caPath)
// 		if err != nil {
// 			return nil, err
// 		}
// 		if ok := rootCertPool.AppendCertsFromPEM(pem); !ok {
// 			return nil, fmt.Errorf("failed to append root CA cert at %s", caPath)
// 		}
// 		opt.TLSConfig = &tls.Config{
// 			RootCAs: rootCertPool,
// 		}

// 		// https://pkg.go.dev/crypto/tls#LoadX509KeyPair
// 		clientCert, ok := os.LookupEnv("TLS_CERT_FILE")
// 		clientKey, ok2 := os.LookupEnv("TLS_KEY_FILE")
// 		if ok && ok2 {
// 			cert, err := tls.LoadX509KeyPair(clientCert, clientKey)
// 			if err != nil {
// 				return nil, err
// 			}
// 			opt.TLSConfig.Certificates = []tls.Certificate{cert}
// 			opt.TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert
// 			opt.TLSConfig.ClientCAs = rootCertPool
// 		}
// 	}

// 	return opt, nil
// }
