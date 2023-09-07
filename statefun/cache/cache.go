// Copyright 2023 NJWS Inc.

// Foliage statefun cache package.
// Provides cache system that lives between stateful functions and NATS key/value
package cache

import (
	"context"
	"encoding/binary"
	"fmt"
	"json_easy"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/foliagecp/sdk/statefun/system"
	"github.com/nats-io/nats.go"
)

type KeyValue struct {
	Key   any
	Value any
}

type CacheStoreValue struct {
	parent                         *CacheStoreValue
	keyInParent                    any
	value                          any
	valueExists                    bool
	purgeState                     int // 0 - do not purge, 1 - wait for KV update confirmation and go to state 2, 2 - purge
	store                          map[any]*CacheStoreValue
	storeConsistencyWithKVLossTime int64 // "0" if store contains all keys and all subkeys (no lru purged ones at any next level)
	valueUpdateTime                int64
	storeMutex                     *sync.Mutex
	notifyUpdates                  sync.Map
	syncNeeded                     bool
	syncedWithKV                   bool
}

func notifySubscriber(c chan KeyValue, key any, value any) {
	guaranteedDelivery := func(channel chan KeyValue, k any, v any) {
		channel <- KeyValue{Key: k, Value: v}
	}
	if len(c) < cap(c) { // If room is available in th channel
		guaranteedDelivery(c, key, value)
	} else {
		go guaranteedDelivery(c, key, value) // Runs in a separate thread cause notifySubscriber must be non blocking operation
	}
}

func (csv *CacheStoreValue) Lock(caller string) {
	//fmt.Printf("------- Locking '%s' by '%s'\n", csv.keyInParent, caller)
	csv.storeMutex.Lock()
	//fmt.Printf(">>>>>>> Locked '%s' by '%s'\n", csv.keyInParent, caller)
}

func (csv *CacheStoreValue) Unlock(caller string) {
	//fmt.Printf(">>>>>>> Unlocking '%s' by '%s'\n", csv.keyInParent, caller)
	csv.storeMutex.Unlock()
	//fmt.Printf("------- Unlocked '%s' by '%s'\n", csv.keyInParent, caller)
}

func (csv *CacheStoreValue) GetFullKeyString() string {
	if csv.parent != nil {
		if keyStr, ok := csv.keyInParent.(string); ok {
			prefix := csv.parent.GetFullKeyString()
			if len(prefix) > 0 {
				return csv.parent.GetFullKeyString() + "." + keyStr
			}
			return keyStr
		}
	} else {
		if keyStr, ok := csv.keyInParent.(string); ok {
			return keyStr
		}
	}
	return ""
}

func (csv *CacheStoreValue) ConsistencyLoss(lossTime int64) {
	if lossTime > atomic.LoadInt64(&csv.storeConsistencyWithKVLossTime) {
		atomic.StoreInt64(&csv.storeConsistencyWithKVLossTime, lossTime)
	}
	if csv.parent != nil {
		csv.parent.ConsistencyLoss(lossTime)
	}
}

func (csv *CacheStoreValue) ValueExists() bool {
	return csv.valueExists
}

func (csv *CacheStoreValue) LoadChild(key any, safe bool) (*CacheStoreValue, bool) {
	if safe {
		csv.Lock("LoadChild")
		defer csv.Unlock("LoadChild")
	}
	if v, ok := csv.store[key]; ok {
		return v, true
	}
	return nil, false
}

func (csv *CacheStoreValue) StoreChild(key any, child *CacheStoreValue, safe bool) {
	child.Lock("StoreChild child")
	defer child.Unlock("StoreChild child")

	child.parent = csv
	child.keyInParent = key

	if safe {
		csv.Lock("StoreChild")
	}
	csv.store[key] = child
	if safe {
		csv.Unlock("StoreChild")
	}
	csv.notifyUpdates.Range(func(_, v any) bool {
		notifySubscriber(v.(chan KeyValue), key, child.value)
		return true
	})
}

func (csv *CacheStoreValue) Put(value any, updateInKV bool, customPutTime int64) {
	csv.Lock("Put")
	key := csv.keyInParent

	csv.value = value
	csv.valueExists = true
	csv.purgeState = 0
	if customPutTime < 0 {
		customPutTime = system.GetCurrentTimeNs()
	}
	csv.valueUpdateTime = customPutTime
	csv.syncNeeded = updateInKV
	csv.syncedWithKV = !updateInKV

	if csv.parent != nil {
		csv.parent.notifyUpdates.Range(func(k, v any) bool {
			notifySubscriber(v.(chan KeyValue), key, value)
			return true
		})
	}

	csv.Unlock("Put")
}

func (csv *CacheStoreValue) collectGarbage() {
	var canBeDeletedFromParent bool

	csv.Lock("collectGarbage")

	if !csv.valueExists && len(csv.store) == 0 && csv.syncedWithKV {
		csv.TryPurgeReady(false)
		csv.TryPurgeConfirm(false)
	}

	noNotifySubscribers := true
	csv.notifyUpdates.Range(func(k, v any) bool {
		noNotifySubscribers = false
		return false
	})
	canBeDeletedFromParent = csv.purgeState == 2 && len(csv.store) == 0 && !csv.syncNeeded && csv.syncedWithKV && noNotifySubscribers
	csv.Unlock("collectGarbage")

	if csv.parent != nil && canBeDeletedFromParent {
		csv.parent.Lock("collectGarbageParent")
		delete(csv.parent.store, csv.keyInParent)
		//fmt.Println("____________ PURGING " + fmt.Sprintln(csv.keyInParent))
		csv.parent.Unlock("collectGarbageParent")
		go csv.parent.collectGarbage()
	}
}

func (csv *CacheStoreValue) TryPurgeReady(safe bool) bool {
	if safe {
		csv.Lock("TryPurgeReady")
		defer csv.Unlock("TryPurgeReady")
	}
	if csv.purgeState == 0 {
		csv.purgeState = 1
		return true
	}
	return false
}

func (csv *CacheStoreValue) TryPurgeConfirm(safe bool) bool {
	if safe {
		csv.Lock("TryPurgeConfirm")
		defer csv.Unlock("TryPurgeConfirm")
	}
	if !csv.syncNeeded && csv.syncedWithKV && csv.purgeState == 1 {
		csv.purgeState = 2
		return true
	}
	return false
}

func (csv *CacheStoreValue) Delete(updateInKV bool, customDeleteTime int64) {
	csv.Lock("Delete")
	key := csv.keyInParent
	// Cannot really remove this value from the parent's store map beacause of the time comparison when updates come from NATS KV
	csv.value = nil
	csv.valueExists = false
	if customDeleteTime < 0 {
		customDeleteTime = system.GetCurrentTimeNs()
	}
	csv.valueUpdateTime = customDeleteTime
	if updateInKV {
		csv.purgeState = 1
		csv.syncNeeded = true
		csv.syncedWithKV = false
	} else {
		csv.purgeState = 2
		csv.syncNeeded = false
		csv.syncedWithKV = true
	}
	csv.Unlock("Delete")

	if csv.parent != nil {
		csv.parent.notifyUpdates.Range(func(k, v any) bool {
			notifySubscriber(v.(chan KeyValue), key, nil)
			return true
		})
	}
}

func (csv *CacheStoreValue) Range(f func(key, value any) bool) {
	csv.Lock("Range")
	defer csv.Unlock("Range")
	for key, value := range csv.store {
		if !f(key, value) {
			break
		}
	}
}

type CacheTransactionOperator struct {
	operatorType int // 0 - set, 1 - delete
	key          string
	value        []byte
	updateInKV   bool
	customTime   int64
}

type CacheTransaction struct {
	operators    []*CacheTransactionOperator
	beginCounter int
	mutex        *sync.Mutex
}

type CacheStore struct {
	cacheConfig *CacheConfig
	kv          nats.KeyValue
	ctx         context.Context
	cancel      context.CancelFunc

	initChan        chan bool
	rootValue       *CacheStoreValue
	lruTresholdTime int64
	valuesInCache   int

	transactions                sync.Map
	transactionsMutex           *sync.Mutex
	getKeysByPatternFromKVMutex *sync.Mutex
}

func NewCacheStore(cacheConfig *CacheConfig, ctx context.Context, kv nats.KeyValue) *CacheStore {
	cs := CacheStore{
		cacheConfig:                 cacheConfig,
		kv:                          kv,
		initChan:                    make(chan bool),
		rootValue:                   &CacheStoreValue{parent: nil, value: nil, storeMutex: &sync.Mutex{}, store: make(map[any]*CacheStoreValue), storeConsistencyWithKVLossTime: 0, valueExists: false, purgeState: 0, syncNeeded: false, syncedWithKV: true, valueUpdateTime: -1},
		lruTresholdTime:             0,
		valuesInCache:               0,
		transactionsMutex:           &sync.Mutex{},
		getKeysByPatternFromKVMutex: &sync.Mutex{},
	}

	cs.ctx, cs.cancel = context.WithCancel(ctx)

	storeUpdatesHandler := func(cs *CacheStore) {
		for {
			select {
			case <-cs.ctx.Done():
			default:
				if w, err := kv.Watch(cacheConfig.kvStorePrefix + ".>"); err == nil {
					for entry := range w.Updates() {
						if entry != nil {
							key := cs.fromStoreKey(entry.Key())
							valueBytes := entry.Value()
							if len(valueBytes) >= 9 { // Update or delete signal from KV store
								appendFlag := valueBytes[8]
								kvRecordTime := int64(binary.BigEndian.Uint64(valueBytes[:8]))

								cacheRecordTime := cs.GetValueUpdateTime(key)
								if kvRecordTime > cacheRecordTime {
									if appendFlag == 1 {
										//fmt.Printf("---CACHE_KV TF UPDATE: %s, %d, %d\n", key, kvRecordTime, appendFlag)
										cs.SetValue(key, valueBytes[9:], false, kvRecordTime, "")
									} else { // Someone else (other module) deleted a key from the cache
										//fmt.Printf("---CACHE_KV TF DELETE: %s, %d, %d\n", key, kvRecordTime, appendFlag)
										kv.Delete(entry.Key())

										//cs.rootValue.purgeReady
										//if csv := cs.getLastKeyCacheStoreValue(key); csv != nil {
										//	csv.Purge(true)
										//}
									}
								} else if kvRecordTime == cacheRecordTime { // KV confirmes update
									if appendFlag == 0 {
										kv.Delete(entry.Key())
									}
									if csv := cs.getLastKeyCacheStoreValue(key); csv != nil {
										csv.Lock("storeUpdatesHandler")
										csv.syncedWithKV = true
										csv.TryPurgeConfirm(false)
										csv.Unlock("storeUpdatesHandler")
									}
									//fmt.Printf("---CACHE_KV TF TOO OLD: %s, %d, %d\n", key, kvRecordTime, appendFlag)
								}
							} else if len(valueBytes) == 0 { // Complete delete signal from KV store
								if csv := cs.getLastKeyCacheStoreValue(key); csv != nil {
									csv.Lock("storeUpdatesHandler complete_delete")
									csv.syncedWithKV = true
									csv.TryPurgeReady(false)
									csv.TryPurgeConfirm(false)
									csv.Unlock("storeUpdatesHandler complete_delete")
								}
								//fmt.Printf("---CACHE_KV EMPTY: %s\n", key)
								// Deletion notify - omitting cause value must already be deleted from the cache
							} else {
								//fmt.Printf("---CACHE_KV !T!F: %s\n", key)
								fmt.Printf("ERROR storeUpdatesHandler: received value without time and append flag!\n")
							}
						} else {
							close(cs.initChan)
						}
					}
				} else {
					fmt.Printf("storeUpdatesHandler kv.Watch error %s\n", err)
				}
			}
			time.Sleep(100 * time.Millisecond) // Prevents too much processor time consumption
		}
	}
	kvLazyWriter := func(cs *CacheStore) {
		for {
			select {
			case <-cs.ctx.Done():
			default:
				cacheStoreValueStack := []*CacheStoreValue{cs.rootValue}
				suffixPathsStack := []string{""}
				depthsStack := []int{0}

				lruTimes := []int64{}

				for len(cacheStoreValueStack) > 0 {
					lastId := len(cacheStoreValueStack) - 1

					currentStoreValue := cacheStoreValueStack[lastId]

					currentStoreValue.Lock("kvLazyWriter")
					lruTimes = append(lruTimes, currentStoreValue.valueUpdateTime)
					currentStoreValue.Unlock("kvLazyWriter")

					currentSuffix := suffixPathsStack[lastId]
					currentDepth := depthsStack[lastId]

					cacheStoreValueStack = cacheStoreValueStack[:lastId]
					suffixPathsStack = suffixPathsStack[:lastId]
					depthsStack = depthsStack[:lastId]

					noChildred := true
					currentStoreValue.Range(func(key, value any) bool {
						noChildred = false

						var newSuffix string
						if currentDepth == 0 {
							newSuffix = currentSuffix + key.(string)
						} else {
							newSuffix = currentSuffix + "." + key.(string)
						}

						var finalBytes []byte = nil

						csvChild := value.(*CacheStoreValue)
						var valueUpdateTime int64 = 0
						csvChild.Lock("kvLazyWriter")
						if csvChild.syncNeeded {
							valueUpdateTime = csvChild.valueUpdateTime
							timeBytes := make([]byte, 8)
							binary.BigEndian.PutUint64(timeBytes, uint64(csvChild.valueUpdateTime))
							if csvChild.valueExists {
								header := append(timeBytes, 1) // Add append flag "1"
								finalBytes = append(header, csvChild.value.([]byte)...)
							} else {
								finalBytes = append(timeBytes, 0) // Add delete flag "0"
							}
						} else {
							if csvChild.valueUpdateTime > 0 && csvChild.valueUpdateTime <= cs.lruTresholdTime && csvChild.purgeState == 0 { // Older than or equal to specific time
								// currentStoreValue locked by range no locking/unlocking needed
								currentStoreValue.ConsistencyLoss(system.GetCurrentTimeNs())
								//fmt.Printf("Consistency lost for key=\"%s\" store\n", currentStoreValue.GetFullKeyString())
								//fmt.Println("Purging: " + newSuffix)
								csvChild.TryPurgeReady(false)
								csvChild.TryPurgeConfirm(false)
							}
						}
						csvChild.Unlock("kvLazyWriter")

						// Putting value into KV store ------------------
						if csvChild.syncNeeded {
							keyStr := key.(string)
							_, putErr := kv.Put(cs.toStoreKey(newSuffix), finalBytes)
							if putErr == nil {
								csvChild.Lock("kvLazyWriter")
								if valueUpdateTime == csvChild.valueUpdateTime {
									csvChild.syncNeeded = false
								}
								csvChild.Unlock("kvLazyWriter")
							} else {
								fmt.Printf("CacheStore kvLazyWriter cannot update key=%s\n: %s", keyStr, putErr)
							}
						}
						// ----------------------------------------------

						cacheStoreValueStack = append(cacheStoreValueStack, value.(*CacheStoreValue))
						suffixPathsStack = append(suffixPathsStack, newSuffix)
						depthsStack = append(depthsStack, currentDepth+1)
						return true
					})

					if noChildred {
						currentStoreValue.collectGarbage()
					}
				}

				sort.Slice(lruTimes, func(i, j int) bool { return lruTimes[i] > lruTimes[j] })
				if len(lruTimes) > cacheConfig.lruSize {
					cs.lruTresholdTime = lruTimes[cacheConfig.lruSize-1]
				} else {
					cs.lruTresholdTime = lruTimes[len(lruTimes)-1]
				}

				/*// Debug info -----------------------------------------------------
				if cs.valuesInCache != len(lruTimes) {
					cmpr := []bool{}
					for i := 0; i < len(lruTimes); i++ {
						cmpr = append(cmpr, lruTimes[i] > 0 && lruTimes[i] <= cs.lruTresholdTime)
					}
					fmt.Printf("LEFT IN CACHE: %d (%d) - %s %s\n", len(lruTimes), cs.lruTresholdTime, fmt.Sprintln(cmpr), fmt.Sprintln(lruTimes))
				}
				// ----------------------------------------------------------------*/

				cs.valuesInCache = len(lruTimes)

				time.Sleep(100 * time.Millisecond) // Prevents too many locks and prevents too much processor time consumption
			}
		}
	}
	go storeUpdatesHandler(&cs)
	go kvLazyWriter(&cs)
	<-cs.initChan
	return &cs
}

// key - level callback key, for e.g. "a.b.c.*"
// callbackId - unique id for this subscription
func (cs *CacheStore) SubscribeLevelCallback(key string, callbackId string) chan KeyValue {
	if _, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, true); parentCacheStoreValue != nil {
		callbackChannel := make(chan KeyValue, cs.cacheConfig.levelSubscriptionChannelSize)
		parentCacheStoreValue.notifyUpdates.Store(callbackId, callbackChannel)
		return callbackChannel
	}
	return nil
}

func (cs *CacheStore) UnsubscribeLevelCallback(key string, callbackId string) {
	if _, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, false); parentCacheStoreValue != nil {
		parentCacheStoreValue.notifyUpdates.Delete(callbackId)
	}
}

func (cs *CacheStore) GetValueUpdateTime(key string) int64 {
	var result int64 = -1

	if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, false); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
		if csv, ok := parentCacheStoreValue.LoadChild(keyLastToken, true); ok {
			csv.Lock("GetValueUpdateTime")
			result = csv.valueUpdateTime
			csv.Unlock("GetValueUpdateTime")
		}
	}
	return result
}

func (cs *CacheStore) GetValue(key string) ([]byte, error) {
	var result []byte = nil
	var resultError error = nil

	cacheMiss := true

	if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, false); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
		if csv, ok := parentCacheStoreValue.LoadChild(keyLastToken, true); ok {
			cacheMiss = false // Value exists in cache - no cache miss then
			csv.Lock("GetValue")
			if csv.ValueExists() {
				if bv, ok := csv.value.([]byte); ok {
					result = bv
				}
			} else { // Value was intenionally deleted and was marked so, no cache miss policy can be applied here
				resultError = fmt.Errorf("Value for for key=%s does not exist", key)
			}
			csv.Unlock("GetValue")
		}
	}

	// Cache miss -----------------------------------------
	if cacheMiss {
		if entry, err := cs.kv.Get(cs.toStoreKey(key)); err == nil {
			key := cs.fromStoreKey(entry.Key())
			valueBytes := entry.Value()
			result = valueBytes[9:]

			if len(valueBytes) >= 9 { // Updated or deleted value exists in KV store
				appendFlag := valueBytes[8]
				kvRecordTime := int64(binary.BigEndian.Uint64(valueBytes[:8]))
				if appendFlag == 1 { // Valid value exists in KV store
					cs.SetValue(key, result, false, kvRecordTime, "")
					resultError = nil
				}
			}
		} else {
			resultError = err
		}
	}
	// ----------------------------------------------------

	return result, resultError
}

func (cs *CacheStore) GetValueAsJSON(key string) (*json_easy.JSON, error) {
	if value, err := cs.GetValue(key); err == nil {
		if j, ok := json_easy.JSONFromBytes(value); ok {
			return &j, nil
		} else {
			return nil, fmt.Errorf("Value for key=%s is not a JSON\n", key)
		}
	} else {
		return nil, err
	}
}

func (cs *CacheStore) TransactionBegin(transactionId string) {
	if v, ok := cs.transactions.Load(transactionId); ok {
		transaction := v.(*CacheTransaction)
		transaction.mutex.Lock()
		transaction.beginCounter++
		transaction.mutex.Unlock()
	} else {
		cs.transactions.Store(transactionId, &CacheTransaction{operators: []*CacheTransactionOperator{}, beginCounter: 1, mutex: &sync.Mutex{}})
	}
}

func (cs *CacheStore) TransactionEnd(transactionId string) {
	if v, ok := cs.transactions.Load(transactionId); ok {
		transaction := v.(*CacheTransaction)
		transaction.mutex.Lock()
		transaction.beginCounter--
		if transaction.beginCounter == 0 {
			cs.transactionsMutex.Lock()
			for _, op := range transaction.operators {
				switch op.operatorType {
				case 0:
					cs.SetValue(op.key, op.value, op.updateInKV, op.customTime, "")
				case 1:
					cs.DeleteValue(op.key, op.updateInKV, op.customTime, "")
				}
			}
			cs.transactionsMutex.Unlock()
			cs.transactions.Delete(transactionId)
		}
		transaction.mutex.Unlock()
	} else {
	}
}

/*func (cs *CacheStore) SetValueIfEquals(key string, newValue []byte, updateInKV bool, customSetTime int64, compareValue []byte) bool {
	if customSetTime < 0 {
		customSetTime = GetCurrentTimeNs()
	}
	if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, true); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
		parentCacheStoreValue.Lock("SetValueIfEquals parent")
		defer parentCacheStoreValue.Unlock("SetValueIfEquals parent")

		var csvUpdate *CacheStoreValue = nil
		if csv, ok := parentCacheStoreValue.LoadChild(keyLastToken, false); ok {
			if currentByteValue, ok := csv.value.([]byte); ok && bytes.Equal(currentByteValue, compareValue) {
				csv.Put(newValue, updateInKV, customSetTime)
				return true
			}
			return false
		} else {
			csvUpdate = &CacheStoreValue{value: newValue, storeMutex: &sync.Mutex{}, store: make(map[any]*CacheStoreValue), storeConsistencyWithKVLossTime: 0, valueExists: true, purgeState: 0, syncNeeded: updateInKV, syncedWithKV: !updateInKV, valueUpdateTime: customSetTime}
			parentCacheStoreValue.StoreChild(keyLastToken, csvUpdate)
			return true
		}
	}
	return false
}*/

func (cs *CacheStore) SetValueIfDoesNotExist(key string, newValue []byte, updateInKV bool, customSetTime int64) bool {
	if customSetTime < 0 {
		customSetTime = system.GetCurrentTimeNs()
	}
	if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, true); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
		parentCacheStoreValue.Lock("SetValueIfEquals parent")
		defer parentCacheStoreValue.Unlock("SetValueIfEquals parent")

		var csvUpdate *CacheStoreValue = nil
		if csv, ok := parentCacheStoreValue.LoadChild(keyLastToken, false); ok {
			if csv.value == nil && csv.valueExists == false {
				csv.Put(newValue, updateInKV, customSetTime)
				return true
			}
		} else {
			csvUpdate = &CacheStoreValue{value: newValue, storeMutex: &sync.Mutex{}, store: make(map[any]*CacheStoreValue), storeConsistencyWithKVLossTime: 0, valueExists: true, purgeState: 0, syncNeeded: updateInKV, syncedWithKV: !updateInKV, valueUpdateTime: customSetTime}
			parentCacheStoreValue.StoreChild(keyLastToken, csvUpdate, false)
			return true
		}
	}
	return false
}

func (cs *CacheStore) SetValue(key string, value []byte, updateInKV bool, customSetTime int64, transactionId string) {
	if customSetTime < 0 {
		customSetTime = system.GetCurrentTimeNs()
	}
	if len(transactionId) == 0 {
		//fmt.Println(">>1 " + key)
		if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, true); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
			//fmt.Println(">>2 " + key)
			var csvUpdate *CacheStoreValue = nil
			if csv, ok := parentCacheStoreValue.LoadChild(keyLastToken, true); ok {
				//fmt.Println(">>3 " + key)
				csv.Put(value, updateInKV, customSetTime)
			} else {
				//fmt.Println(">>4 " + key)
				csvUpdate = &CacheStoreValue{value: value, storeMutex: &sync.Mutex{}, store: make(map[any]*CacheStoreValue), storeConsistencyWithKVLossTime: 0, valueExists: true, purgeState: 0, syncNeeded: updateInKV, syncedWithKV: !updateInKV, valueUpdateTime: customSetTime}
				//fmt.Println(">>5 " + key)
				parentCacheStoreValue.StoreChild(keyLastToken, csvUpdate, true)
				//fmt.Println(">>6 " + key)
			}
		}
	} else {
		if v, ok := cs.transactions.Load(transactionId); ok {
			transaction := v.(*CacheTransaction)
			transaction.mutex.Lock()
			transaction.operators = append(transaction.operators, &CacheTransactionOperator{operatorType: 0, key: key, value: value, updateInKV: updateInKV, customTime: customSetTime})
			transaction.mutex.Unlock()
		} else {
			fmt.Printf("ERROR SetValue: transaction with id=%s doesn't exist\n", transactionId)
		}
	}
}

func (cs *CacheStore) Destroy() {
	cs.cancel()
}

func (cs *CacheStore) DeleteValue(key string, updateInKV bool, customDeleteTime int64, transactionId string) {
	if customDeleteTime < 0 {
		customDeleteTime = system.GetCurrentTimeNs()
	}
	if len(transactionId) == 0 {
		if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(key, false); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
			if csv, ok := parentCacheStoreValue.LoadChild(keyLastToken, true); ok {
				if csv.valueExists {
					csv.Delete(updateInKV, customDeleteTime)
				}
			}
		}
	} else {
		if v, ok := cs.transactions.Load(transactionId); ok {
			transaction := v.(*CacheTransaction)
			transaction.mutex.Lock()
			transaction.operators = append(transaction.operators, &CacheTransactionOperator{operatorType: 1, key: key, value: nil, updateInKV: updateInKV, customTime: customDeleteTime})
			transaction.mutex.Unlock()
		} else {
			fmt.Printf("ERROR DeleteValue: transaction with id=%s doesn't exist\n", transactionId)
		}
	}
}

func (cs *CacheStore) GetKeysByPattern(pattern string) []string {
	// TODO: Restore cs.consistentWithKV to true: all keys from KV must exist in cache

	keys := map[string]bool{}

	appendKeysFromKV := func() {
		cs.getKeysByPatternFromKVMutex.Lock()
		//fmt.Println("!!! GetKeysByPattern started appendKeysFromKV")
		if w, err := cs.kv.Watch(cs.toStoreKey(pattern)); err == nil {
			for entry := range w.Updates() {
				if entry != nil && len(entry.Value()) >= 9 {
					keys[cs.fromStoreKey(entry.Key())] = true
				} else {
					break
				}
			}
		} else {
			fmt.Printf("GetKeysByPattern kv.Watch error %s\n", err)
		}
		//fmt.Println("!!! GetKeysByPattern ended appendKeysFromKV")
		cs.getKeysByPatternFromKVMutex.Unlock()
	}

	if keyLastToken, parentCacheStoreValue := cs.getLastKeyTokenAndItsParentCacheStoreValue(pattern, false); len(keyLastToken) > 0 && parentCacheStoreValue != nil {
		keyWithoutLastToken := pattern[:len(pattern)-1]
		if keyLastToken == "*" {
			// Gettting time of when CSV became inconsistent with KV
			consistencyWithKVLossTime := atomic.LoadInt64(&parentCacheStoreValue.storeConsistencyWithKVLossTime)
			// ------------------------------------------------------

			childrenStoresAreConsistentWithKV := true
			parentCacheStoreValue.Range(func(key, value any) bool {
				childCSV := value.(*CacheStoreValue)
				if atomic.LoadInt64(&childCSV.storeConsistencyWithKVLossTime) > 0 { // Child store not consistent with KV
					childrenStoresAreConsistentWithKV = false
				}
				if childCSV.ValueExists() {
					keys[keyWithoutLastToken+key.(string)] = true
				}
				return true
			})

			// If CSV is inconsistent with KV -----------------------
			if consistencyWithKVLossTime > 0 {
				keysCountBefore := len(keys)
				appendKeysFromKV()
				keysCountAfter := len(keys)

				if keysCountBefore == keysCountAfter && childrenStoresAreConsistentWithKV {
					// Restore consistency if relevant
					if atomic.CompareAndSwapInt64(&parentCacheStoreValue.storeConsistencyWithKVLossTime, consistencyWithKVLossTime, 0) {
						//fmt.Printf("Consistency restored for key=\"%s\" store\n", parentCacheStoreValue.GetFullKeyString())
					}
				}
			}
			// ------------------------------------------------------
		} else if keyLastToken == ">" {
			// Remembering all CSVs on all sub levels which are inconsistent with KV ----
			allSubCSVsToConsistencyWithKVLossTime := map[*CacheStoreValue]int64{}
			inconsistencyWithKVExistsOnSubLevel := false
			// --------------------------------------------------------------------------

			cacheStoreValueStack := []*CacheStoreValue{parentCacheStoreValue}
			suffixPathsStack := []string{keyWithoutLastToken}
			depthsStack := []int{0}
			for len(cacheStoreValueStack) > 0 {
				lastId := len(cacheStoreValueStack) - 1

				currentStoreValue := cacheStoreValueStack[lastId]
				currentSuffix := suffixPathsStack[lastId]
				currentDepth := depthsStack[lastId]

				storeConsistencyWithKVLossTime := atomic.LoadInt64(&currentStoreValue.storeConsistencyWithKVLossTime)
				if storeConsistencyWithKVLossTime > 0 {
					allSubCSVsToConsistencyWithKVLossTime[currentStoreValue] = storeConsistencyWithKVLossTime
					inconsistencyWithKVExistsOnSubLevel = true
				}

				cacheStoreValueStack = cacheStoreValueStack[:lastId]
				suffixPathsStack = suffixPathsStack[:lastId]
				depthsStack = depthsStack[:lastId]

				currentStoreValue.Range(func(key, value any) bool {
					var newSuffix string
					if currentDepth == 0 {
						newSuffix = currentSuffix + key.(string)
					} else {
						newSuffix = currentSuffix + "." + key.(string)
					}
					if value.(*CacheStoreValue).ValueExists() {
						keys[newSuffix] = true
					}
					cacheStoreValueStack = append(cacheStoreValueStack, value.(*CacheStoreValue))
					suffixPathsStack = append(suffixPathsStack, newSuffix)
					depthsStack = append(depthsStack, currentDepth+1)
					return true
				})
			}

			// If CSV is inconsistent with KV -----------------------
			if inconsistencyWithKVExistsOnSubLevel {
				keysCountBefore := len(keys)
				appendKeysFromKV()
				keysCountAfter := len(keys)

				if keysCountBefore == keysCountAfter {
					for subCSV, subCSVConsistencyWithKVLossTime := range allSubCSVsToConsistencyWithKVLossTime {
						// Restore consistency if relevant
						if atomic.CompareAndSwapInt64(&subCSV.storeConsistencyWithKVLossTime, subCSVConsistencyWithKVLossTime, 0) {
							//fmt.Printf("Consistency restored for key=\"%s\" store\n", subCSV.GetFullKeyString())
						}
					}
				}
			}
			// ------------------------------------------------------
		} else {
			// Gettting time of when CSV became inconsistent with KV
			consistencyWithKVLossTime := atomic.LoadInt64(&parentCacheStoreValue.storeConsistencyWithKVLossTime)
			// ------------------------------------------------------

			if _, ok := parentCacheStoreValue.LoadChild(keyLastToken, true); ok {
				keys[pattern] = true
			}

			// If CSV is inconsistent with KV -----------------------
			if consistencyWithKVLossTime > 0 {
				appendKeysFromKV()
				// Cannot restore consistency here cause not checking all keys from KV for this CSV level
			}
			// ------------------------------------------------------
		}
	} else { // No CSV at the level corresponding to the pattern at all
		if ancestorCacheStoreValue := cs.getLastExistingCacheStoreValueByKey(pattern); ancestorCacheStoreValue != nil {
			if atomic.LoadInt64(&ancestorCacheStoreValue.storeConsistencyWithKVLossTime) > 0 {
				appendKeysFromKV()
				// Cannot restore consistency here
			}
		} else {
			fmt.Printf("ERROR GetKeysByPattern: getLastExistingCacheStoreValueByKey returns nil\n")
		}
	}

	keysSlice := make([]string, len(keys))
	i := 0
	for k := range keys {
		keysSlice[i] = k
		i++
	}
	return keysSlice
}

// createIfNotexistsOption - 0 // Do not create, 1 // Create non parent CacheStoreValue thread safe, 2 // Create parent CacheStoreValue thread safe
func (cs *CacheStore) getLastKeyTokenAndItsParentCacheStoreValue(key string, createIfNotexists bool) (string, *CacheStoreValue) {
	tokens := strings.Split(key, ".")
	currentTokenId := 0
	currentStoreLevel := cs.rootValue
	for currentTokenId < len(tokens)-1 {
		if csv, ok := currentStoreLevel.LoadChild(tokens[currentTokenId], true); ok {
			currentStoreLevel = csv
		} else {
			if createIfNotexists {
				csv := CacheStoreValue{value: nil, storeMutex: &sync.Mutex{}, store: make(map[any]*CacheStoreValue), storeConsistencyWithKVLossTime: 0, valueExists: false, purgeState: 0, syncNeeded: false, syncedWithKV: true, valueUpdateTime: system.GetCurrentTimeNs()}
				currentStoreLevel.StoreChild(tokens[currentTokenId], &csv, true)
				currentStoreLevel = &csv
			} else {
				return "", nil
			}
		}
		currentTokenId++
	}
	return tokens[currentTokenId], currentStoreLevel
}

func (cs *CacheStore) getLastExistingCacheStoreValueByKey(key string) *CacheStoreValue {
	tokens := strings.Split(key, ".")
	currentTokenId := 0
	currentStoreLevel := cs.rootValue

	for currentTokenId < len(tokens)-1 {
		if csv, ok := currentStoreLevel.LoadChild(tokens[currentTokenId], true); ok {
			currentStoreLevel = csv
		} else {
			break
		}
		currentTokenId++
	}

	return currentStoreLevel
}

func (cs *CacheStore) getLastKeyCacheStoreValue(key string) *CacheStoreValue {
	tokens := strings.Split(key, ".")
	currentTokenId := 0
	currentStoreLevel := cs.rootValue
	for currentTokenId < len(tokens) {
		if csv, ok := currentStoreLevel.LoadChild(tokens[currentTokenId], true); ok {
			currentStoreLevel = csv
		} else {
			return nil
		}
		currentTokenId++
	}
	return currentStoreLevel
}

func (cs *CacheStore) toStoreKey(key string) string {
	return cs.cacheConfig.kvStorePrefix + "." + key
}

func (cs *CacheStore) fromStoreKey(key string) string {
	return strings.Replace(key, cs.cacheConfig.kvStorePrefix+".", "", 1)
}
