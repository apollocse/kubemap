package kubemap

import (
	"fmt"
	"log"

	"k8s.io/client-go/tools/cache"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/workqueue"
)

const maxRetries = 5

//NewMapper creates a Mapper to map interlinked K8s resources
func NewMapper() *Mapper {
	store := cache.NewStore(metaResourceKeyFunc)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	return &Mapper{
		store: store,
		queue: queue,
	}
}

//NewMapperWithOptions creates a Mapper to map interlinked K8s resources with custom options
func NewMapperWithOptions(options MapOptions) (*Mapper, error) {
	store := cache.NewStore(metaResourceKeyFunc)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	zapLogger, zapErr := getZapLogger(options.Logging.LogLevel)
	if zapErr != nil {
		return nil, zapErr
	}

	return &Mapper{
		store: store,
		queue: queue,
		log: Logger{
			enabled: options.Logging.Enabled,
			logger:  zapLogger,
		},
	}, nil
}

//NewStoreMapper created a mapper that works with existing store.
func NewStoreMapper(store cache.Store) *Mapper {
	return &Mapper{
		store: store,
	}
}

//NewStoreMapperWithOptions created a mapper that works with existing store.
func NewStoreMapperWithOptions(store cache.Store, options MapOptions) (*Mapper, error) {
	zapLogger, zapErr := getZapLogger(options.Logging.LogLevel)
	if zapErr != nil {
		return nil, zapErr
	}

	return &Mapper{
		store: store,
		log: Logger{
			enabled: options.Logging.Enabled,
			logger:  zapLogger,
		},
	}, nil
}

//StoreMap gets a resources and maps it with exiting resources in store
func (m *Mapper) StoreMap(obj interface{}) ([]MapResult, error) {
	mapResults, err := m.kubemapper(obj, m.store)
	if err != nil {
		return []MapResult{}, err
	}

	return mapResults, nil
}

//StoreMapObj gets a resources and maps it with exiting resources in store
func (m *Mapper) StoreMapObj(obj interface{}) ([]MapResult, error) {
	mapResults, err := m.kubemapper(obj, m.store)
	if err != nil {
		m.error(fmt.Sprintf("Cannot map resources - %v", err))
		return []MapResult{}, err
	}

	return mapResults, nil
}

//Map accepts collection different k8s resources.
//They will be mapped to respective common label and returned
func (m *Mapper) Map(resources KubeResources) (MappedResources, error) {
	addResourcesForMapping(resources, m.queue)

	mappedResources := m.runMap(m.queue, m.store)

	return mappedResources, nil
}

//RunMap starts mapper controller
func (m *Mapper) runMap(queue workqueue.RateLimitingInterface, store cache.Store) MappedResources {
	defer utilruntime.HandleCrash()
	defer queue.ShutDown()

	m.runMapWorker(queue, store)

	return getAllMappedResources(store)
}

func (m *Mapper) runMapWorker(queue workqueue.RateLimitingInterface, store cache.Store) {
	for { // Process until there are no messages in queue.
		if queue.Len() > 0 {
			m.processNextItemToMap(queue, store)
		} else {
			break
		}
	}
}

func (m *Mapper) processNextItemToMap(queue workqueue.RateLimitingInterface, store cache.Store) bool {
	obj, quit := queue.Get()
	if quit {
		return false
	}
	defer queue.Done(obj)
	err := m.processK8sItem(obj, store)
	if err == nil {
		// No error, reset the ratelimit counters
		queue.Forget(obj)
	} else if queue.NumRequeues(obj) < maxRetries {
		queue.AddRateLimited(obj)
	} else {
		// err != nil and too many retries
		queue.Forget(obj)
		utilruntime.HandleError(err)

		m.warn(fmt.Sprintf("\nToo many retries. Forgetting message from queue.\n"))
	}

	return true
}

func (m *Mapper) processK8sItem(obj interface{}, store cache.Store) error {
	_, err := m.kubemapper(obj, store)
	if err != nil {
		m.error(fmt.Sprintf("\nCannot map resources - %v\n", err))
		return err
	}

	return nil
}

func getAllMappedResources(store cache.Store) MappedResources {
	var mappedResources MappedResources
	keys := store.ListKeys()
	for _, key := range keys {
		item, _, _ := store.GetByKey(key)
		mappedResource := item.(MappedResource)
		mappedResources.MappedResource = append(mappedResources.MappedResource, mappedResource)
	}

	return mappedResources
}

func addResourcesForMapping(resources KubeResources, queue workqueue.RateLimitingInterface) {
	//Add ingresses
	for _, ingress := range resources.Ingresses {
		queue.Add(gerResourceEvent(ingress.DeepCopy(), "ingress"))
	}

	//Add services
	for _, service := range resources.Services {
		queue.Add(gerResourceEvent(service.DeepCopy(), "service"))
	}

	//Add deployments
	for _, deployment := range resources.Deployments {
		queue.Add(gerResourceEvent(deployment.DeepCopy(), "deployment"))
	}

	//Add replica sets
	for _, replicaSet := range resources.ReplicaSets {
		queue.Add(gerResourceEvent(replicaSet.DeepCopy(), "replicaset"))
	}

	//Add pods
	for _, pod := range resources.Pods {
		queue.Add(gerResourceEvent(pod.DeepCopy(), "pod"))
	}
}

func gerResourceEvent(obj interface{}, resourceType string) ResourceEvent {
	var newResourceEvent ResourceEvent
	var err error

	objMeta := objectMetaData(obj)
	newResourceEvent.UID = string(objMeta.UID)
	newResourceEvent.Key, err = cache.MetaNamespaceKeyFunc(obj)
	newResourceEvent.EventType = "ADDED"
	newResourceEvent.ResourceType = resourceType
	newResourceEvent.Namespace = objMeta.Namespace
	newResourceEvent.Name = objMeta.Name
	newResourceEvent.Event = obj
	//newResourceEvent.RawObj = obj

	if err != nil {
		log.Fatalf("Can't get key for store")
	}

	return newResourceEvent
}
