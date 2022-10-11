package distributor

import (
	"context"
	"testing"
	"time"

	"github.com/grafana/loki/pkg/distributor/shardstreams"
	"github.com/grafana/loki/pkg/validation"

	"github.com/stretchr/testify/require"

	client2 "github.com/grafana/loki/pkg/ingester/client"

	"google.golang.org/grpc"

	"github.com/grafana/loki/pkg/logproto"

	"github.com/grafana/dskit/ring"
	"github.com/grafana/dskit/ring/client"
)

func TestRateStore(t *testing.T) {
	t.Run("it reports rates from all of the ingesters", func(t *testing.T) {
		tc := setup(true)
		tc.ring.replicationSet = ring.ReplicationSet{
			Instances: []ring.InstanceDesc{
				{Addr: "ingester0"},
				{Addr: "ingester1"},
				{Addr: "ingester2"},
				{Addr: "ingester3"},
			},
		}

		tc.clientPool.clients = map[string]client.PoolClient{
			"ingester0": newRateClient([]*logproto.StreamRate{{
				StreamHash: 0, StreamHashNoShard: 0, Rate: 15}}),
			"ingester1": newRateClient([]*logproto.StreamRate{{
				StreamHash: 1, StreamHashNoShard: 1, Rate: 25}}),
			"ingester2": newRateClient([]*logproto.StreamRate{{
				StreamHash: 2, StreamHashNoShard: 2, Rate: 35}}),
			"ingester3": newRateClient([]*logproto.StreamRate{{
				StreamHash: 3, StreamHashNoShard: 3, Rate: 45}}),
		}

		_ = tc.rateStore.StartAsync(context.Background())
		defer tc.rateStore.StopAsync()

		require.Eventually(t, func() bool { // There will be data
			return tc.rateStore.RateFor(0) != 0
		}, time.Second, time.Millisecond)

		require.Equal(t, int64(15), tc.rateStore.RateFor(0))
		require.Equal(t, int64(25), tc.rateStore.RateFor(1))
		require.Equal(t, int64(35), tc.rateStore.RateFor(2))
		require.Equal(t, int64(45), tc.rateStore.RateFor(3))
	})

	t.Run("it reports the highest rate from replicas", func(t *testing.T) {
		tc := setup(true)
		tc.ring.replicationSet = ring.ReplicationSet{
			Instances: []ring.InstanceDesc{
				{Addr: "ingester0"},
				{Addr: "ingester1"},
				{Addr: "ingester2"},
			},
		}

		tc.clientPool.clients = map[string]client.PoolClient{
			"ingester0": newRateClient([]*logproto.StreamRate{{
				StreamHash: 0, StreamHashNoShard: 0, Rate: 25}}),
			"ingester1": newRateClient([]*logproto.StreamRate{{
				StreamHash: 0, StreamHashNoShard: 0, Rate: 35}}),
			"ingester2": newRateClient([]*logproto.StreamRate{{
				StreamHash: 0, StreamHashNoShard: 0, Rate: 15}}),
		}

		_ = tc.rateStore.StartAsync(context.Background())
		defer tc.rateStore.StopAsync()

		require.Eventually(t, func() bool { // There will be data
			return tc.rateStore.RateFor(0) != 0
		}, time.Second, time.Millisecond)

		require.Equal(t, int64(35), tc.rateStore.RateFor(0))
	})

	t.Run("it aggregates rates over shards", func(t *testing.T) {
		tc := setup(true)
		tc.ring.replicationSet = ring.ReplicationSet{
			Instances: []ring.InstanceDesc{
				{Addr: "ingester0"},
			},
		}

		tc.clientPool.clients = map[string]client.PoolClient{
			"ingester0": newRateClient([]*logproto.StreamRate{
				{StreamHash: 1, StreamHashNoShard: 0, Rate: 25},
				{StreamHash: 2, StreamHashNoShard: 0, Rate: 35},
				{StreamHash: 3, StreamHashNoShard: 0, Rate: 15},
			}),
		}
		_ = tc.rateStore.StartAsync(context.Background())
		defer tc.rateStore.StopAsync()

		require.Eventually(t, func() bool { // There will be data
			return tc.rateStore.RateFor(0) != 0
		}, time.Second, time.Millisecond)

		require.Equal(t, int64(75), tc.rateStore.RateFor(0))
	})

	t.Run("it does nothing if no one has enabled sharding", func(t *testing.T) {
		tc := setup(false)
		tc.ring.replicationSet = ring.ReplicationSet{
			Instances: []ring.InstanceDesc{
				{Addr: "ingester0"},
			},
		}

		tc.clientPool.clients = map[string]client.PoolClient{
			"ingester0": newRateClient([]*logproto.StreamRate{
				{StreamHash: 1, StreamHashNoShard: 0, Rate: 25},
			}),
		}
		_ = tc.rateStore.StartAsync(context.Background())
		defer tc.rateStore.StopAsync()

		time.Sleep(time.Second)
		require.Equal(t, int64(0), tc.rateStore.RateFor(0))
	})
}

func newFakeRing() *fakeRing {
	return &fakeRing{}
}

type fakeRing struct {
	ring.ReadRing

	replicationSet ring.ReplicationSet
	err            error
}

func (r *fakeRing) GetAllHealthy(op ring.Operation) (ring.ReplicationSet, error) {
	return r.replicationSet, r.err
}

func newFakeClientPool() *fakeClientPool {
	return &fakeClientPool{
		clients: make(map[string]client.PoolClient),
	}
}

type fakeClientPool struct {
	clients map[string]client.PoolClient
	err     error
}

func (p *fakeClientPool) GetClientFor(addr string) (client.PoolClient, error) {
	return p.clients[addr], p.err
}

func newRateClient(rates []*logproto.StreamRate) client.PoolClient {
	return client2.ClosableHealthAndIngesterClient{
		StreamDataClient: &fakeStreamDataClient{resp: &logproto.StreamRatesResponse{StreamRates: rates}},
	}
}

type fakeStreamDataClient struct {
	resp *logproto.StreamRatesResponse
	err  error
}

func (c *fakeStreamDataClient) GetStreamRates(ctx context.Context, in *logproto.StreamRatesRequest, opts ...grpc.CallOption) (*logproto.StreamRatesResponse, error) {
	return c.resp, c.err
}

type fakeOverrides struct {
	enabled bool
}

func (c *fakeOverrides) AllByUserID() map[string]*validation.Limits {
	return map[string]*validation.Limits{
		"ingester0": {
			ShardStreams: &shardstreams.Config{
				Enabled: c.enabled,
			},
		},
	}
}

type testContext struct {
	ring       *fakeRing
	clientPool *fakeClientPool
	rateStore  *rateStore
}

func setup(enabled bool) *testContext {
	ring := newFakeRing()
	cp := newFakeClientPool()
	cfg := RateStoreConfig{MaxParallelism: 5, IngesterReqTimeout: time.Second, StreamRateUpdateInterval: 10 * time.Millisecond}

	return &testContext{
		ring:       ring,
		clientPool: cp,
		rateStore:  NewRateStore(cfg, ring, cp, &fakeOverrides{enabled}, nil),
	}
}