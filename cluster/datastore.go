package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/kvtools/valkeyrie/store"
	"github.com/pteich/staert"

	"github.com/pteich/traefik/job"
	"github.com/pteich/traefik/log"
	"github.com/pteich/traefik/safe"
)

// Metadata stores Object plus metadata
type Metadata struct {
	object Object
	Object []byte
	Lock   string
}

// NewMetadata returns new Metadata
func NewMetadata(object Object) *Metadata {
	return &Metadata{object: object}
}

// Marshall marshalls object
func (m *Metadata) Marshall() error {
	var err error
	m.Object, err = json.Marshal(m.object)
	return err
}

func (m *Metadata) unmarshall() error {
	if len(m.Object) == 0 {
		return nil
	}
	return json.Unmarshal(m.Object, m.object)
}

// Listener is called when Object has been changed in KV store
type Listener func(Object) error

var _ Store = (*Datastore)(nil)

// Datastore holds a struct synced in a KV store
type Datastore struct {
	kv        staert.KvSource
	ctx       context.Context
	localLock *sync.RWMutex
	meta      *Metadata
	lockKey   string
	listener  Listener
}

// NewDataStore creates a Datastore
func NewDataStore(ctx context.Context, kvSource staert.KvSource, object Object, listener Listener) (*Datastore, error) {
	datastore := Datastore{
		kv:        kvSource,
		ctx:       ctx,
		meta:      &Metadata{object: object},
		lockKey:   kvSource.Prefix + "/lock",
		localLock: &sync.RWMutex{},
		listener:  listener,
	}
	err := datastore.watchChanges(ctx)
	if err != nil {
		return nil, err
	}
	return &datastore, nil
}

func (d *Datastore) watchChanges(ctx context.Context) error {
	kvCh, err := d.kv.Watch(ctx, d.lockKey, nil)
	if err != nil {
		return fmt.Errorf("error while watching key %s: %v", d.lockKey, err)
	}
	safe.Go(func() {
		ctx, cancel := context.WithCancel(d.ctx)
		defer cancel()
		operation := func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case _, ok := <-kvCh:
					if !ok {
						return err
					}
					err = d.reload()
					if err != nil {
						return err
					}
					if d.listener != nil {
						err := d.listener(d.meta.object)
						if err != nil {
							log.Errorf("Error calling datastore listener: %s", err)
						}
					}
				}
			}
		}
		notify := func(err error, time time.Duration) {
			log.Errorf("Error in watch datastore: %+v, retrying in %s", err, time)
		}
		err := backoff.RetryNotify(safe.OperationWithRecover(operation), job.NewBackOff(backoff.NewExponentialBackOff()), notify)
		if err != nil {
			log.Errorf("Error in watch datastore: %v", err)
		}
	})
	return nil
}

func (d *Datastore) reload() error {
	log.Debug("Datastore reload")
	_, err := d.Load()
	return err
}

// Begin creates a transaction with the KV store.
func (d *Datastore) Begin() (Transaction, Object, error) {
	ctx, cancel := context.WithCancel(d.ctx)
	defer cancel()

	id := uuid.New().String()
	log.Debugf("Transaction %s begins", id)
	remoteLock, err := d.kv.NewLock(ctx, d.lockKey, &store.LockOptions{TTL: 20 * time.Second, Value: []byte(id)})
	if err != nil {
		return nil, nil, err
	}
	var errLock error
	go func() {
		_, errLock = remoteLock.Lock(ctx)
		cancel()
	}()
	select {
	case <-ctx.Done():
		if errLock != nil {
			return nil, nil, errLock
		}
	case <-d.ctx.Done():
		return nil, nil, d.ctx.Err()
	}

	// we got the lock! Now make sure we are synced with KV store
	operation := func() error {
		meta := d.get()
		if meta.Lock != id {
			return fmt.Errorf("object lock value: expected %s, got %s", id, meta.Lock)
		}
		return nil
	}
	notify := func(err error, time time.Duration) {
		log.Errorf("Datastore sync error: %v, retrying in %s", err, time)
		err = d.reload()
		if err != nil {
			log.Errorf("Error reloading: %+v", err)
		}
	}
	ebo := backoff.NewExponentialBackOff()
	ebo.MaxElapsedTime = 60 * time.Second
	err = backoff.RetryNotify(safe.OperationWithRecover(operation), ebo, notify)
	if err != nil {
		return nil, nil, fmt.Errorf("datastore cannot sync: %v", err)
	}

	// we synced with KV store, we can now return Setter
	return &datastoreTransaction{
		Datastore:  d,
		remoteLock: remoteLock,
		id:         id,
	}, d.meta.object, nil
}

func (d *Datastore) get() *Metadata {
	d.localLock.RLock()
	defer d.localLock.RUnlock()
	return d.meta
}

// Load load atomically a struct from the KV store
func (d *Datastore) Load() (Object, error) {
	d.localLock.Lock()
	defer d.localLock.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// clear Object first, as mapstructure's decoder doesn't have ZeroFields set to true for merging purposes
	d.meta.Object = d.meta.Object[:0]

	err := d.kv.LoadConfig(ctx, d.meta)
	if err != nil {
		return nil, err
	}
	err = d.meta.unmarshall()
	if err != nil {
		return nil, err
	}
	return d.meta.object, nil
}

// Get atomically a struct from the KV store
func (d *Datastore) Get() Object {
	d.localLock.RLock()
	defer d.localLock.RUnlock()
	return d.meta.object
}

var _ Transaction = (*datastoreTransaction)(nil)

type datastoreTransaction struct {
	*Datastore
	remoteLock store.Locker
	dirty      bool
	id         string
}

// Commit allows to set an object in the KV store
func (s *datastoreTransaction) Commit(object Object) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.localLock.Lock()
	defer s.localLock.Unlock()
	if s.dirty {
		return fmt.Errorf("transaction already used, please begin a new one")
	}
	s.Datastore.meta.object = object
	err := s.Datastore.meta.Marshall()
	if err != nil {
		return fmt.Errorf("marshall error: %s", err)
	}
	err = s.kv.StoreConfig(ctx, s.Datastore.meta)
	if err != nil {
		return fmt.Errorf("StoreConfig error: %s", err)
	}

	err = s.remoteLock.Unlock(ctx)
	if err != nil {
		return fmt.Errorf("unlock error: %s", err)
	}

	s.dirty = true
	log.Debugf("Transaction committed %s", s.id)
	return nil
}
