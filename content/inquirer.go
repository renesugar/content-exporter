package content

import (
	log "github.com/sirupsen/logrus"
	"fmt"
	"github.com/Financial-Times/content-exporter/db"
)


type Inquirer interface {
	Inquire(collection string, candidates []string) (chan Stub, error, int)
}

type MongoInquirer struct {
	Mongo db.Service
}

func (m *MongoInquirer) Inquire(collection string, candidates []string) (chan Stub, error, int) {
	tx, err := m.Mongo.Open()

	if err != nil {
		return nil, err, 0
	}
	iter, length, err := tx.FindUUIDs(collection, candidates)
	if err != nil {
		tx.Close()
		return nil, err, 0
	}

	docs := make(chan Stub, 8)

	go func() {

		defer tx.Close()
		defer iter.Close()
		defer close(docs)

		var result map[string]interface{}
		counter := 0
		for iter.Next(&result) {
			counter++
            content, err := MapDBContent(result)
			if err != nil {
				log.Warn(err)
				continue
			}
			docs <- content
		}
		log.Infof("Processed %v docs", counter)
	}()

	return docs, nil, length
}

func MapDBContent(result map[string]interface{}) (Stub, error) {
	docUUID, ok := result["uuid"]
	if !ok {
		return Stub{}, fmt.Errorf("No uuid field found in iter result: %v", result)
	}

	return Stub{docUUID.(string), GetDate(result)}, nil
}