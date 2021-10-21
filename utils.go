package main

import (
	"bytes"
	"cloud.google.com/go/datastore"
	"cloud.google.com/go/pubsub"
	"context"
	"encoding/json"
	models "github.com/eahrend/chestermodels"
	log "github.com/sirupsen/logrus"
)

func getProxySQLConfig(instanceGroup string) (*models.ProxySqlConfig, error) {
	psqlConfig := models.NewProxySqlConfig()
	_, err := dsClient.RunInTransaction(context.Background(), func(tx *datastore.Transaction) error {
		key := generateChesterKey(instanceGroup)
		if err := tx.Get(key, psqlConfig); err != nil {
			return err
		}
		return nil
	})
	return psqlConfig, err
}

func generateChesterKey(instanceGroup string) *datastore.Key {
	key := datastore.NameKey("proxysqlconfig", instanceGroup, nil)
	key.Namespace = "chester"
	return key
}

func generateMetaDataKey(parent *datastore.Key) *datastore.Key {
	key := datastore.NameKey(models.MetaData, parent.Name, parent)
	key.Namespace = "chester"
	return key
}

func sendRestartCommand(instanceGroup string) {
	restartCommand := map[string]string{
		"action":              "restart",
		"sql_master_instance": instanceGroup,
	}
	b, err := json.Marshal(&restartCommand)
	if err != nil {
		panic(err)
	}
	msg := pubsub.Message{
		Data: b,
	}
	pubResult := topic.Publish(ctx, &msg)
	_, err = pubResult.Get(context.Background())
	if err != nil {
		panic(err)
	}
}

func getChesterMetadata(instanceGroup string) (models.ChesterMetaData, error) {
	chesterMetaData := models.ChesterMetaData{}
	_, err := dsClient.RunInTransaction(context.Background(), func(tx *datastore.Transaction) error {
		parent := generateChesterKey(instanceGroup)
		key := generateMetaDataKey(parent)
		if err := tx.Get(key, &chesterMetaData); err != nil {
			return err
		}
		return nil
	})
	return chesterMetaData, err
}

func getDatabase(instanceName, filter string) (models.InstanceData, error) {
	psqlconfig, err := getProxySQLConfig(instanceName)
	if err != nil {
		return models.InstanceData{}, err
	}
	var chesterMetaData models.ChesterMetaData
	chesterMetaData, err = getChesterMetadata(instanceName)
	if err != nil {
		log.Errorln("Error getting chester metadata:", err.Error())
	}
	id := models.InstanceData{
		InstanceName:    instanceName,
		ReadHostGroup:   psqlconfig.ReadHostGroup,
		WriteHostGroup:  psqlconfig.WriteHostGroup,
		QueryRules:      psqlconfig.MySqlQueryRules,
		Username:        psqlconfig.MySqlUsers[0].Username,
		Password:        psqlconfig.MySqlUsers[0].Password,
		UseSSL:          psqlconfig.UseSSL,
		ChesterMetaData: chesterMetaData,
	}
	rrs := []models.AddDatabaseRequestDatabaseInformation{}
	var wr models.AddDatabaseRequestDatabaseInformation
	for _, instance := range psqlconfig.MySqlServers {
		if instance.Hostgroup == psqlconfig.ReadHostGroup {
			// Query parameter to ensure we're not adding filtered stuff
			if filter == "true" && instance.Comment == models.AddedByChester {
				continue
			} else {
				rr := models.AddDatabaseRequestDatabaseInformation{
					Name:      instance.Comment,
					IPAddress: instance.Address,
				}
				rrs = append(rrs, rr)
			}
		} else if instance.Hostgroup == psqlconfig.WriteHostGroup {
			wr = models.AddDatabaseRequestDatabaseInformation{
				Name:      instance.Comment,
				IPAddress: instance.Address,
			}
		}
	}
	id.ReadReplicas = rrs
	id.MasterInstance = wr
	return id, nil
}

func updateMetaData(imd map[string]string) error {
	dspsmd := models.InstanceMetaData{}
	b, err := json.Marshal(&imd)
	if err != nil {
		return err
	}
	dspsmd.InstanceMetaData = string(b)
	_, err = dsClient.RunInTransaction(context.Background(), func(tx *datastore.Transaction) error {
		key := datastore.NameKey("proxysql_metadata", "proxysql_metadata", nil)
		key.Namespace = "chester"
		if _, err := tx.Put(key, &dspsmd); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	return err
}

func insertMetaData(instanceName string) error {
	instanceMap := map[string]string{
		instanceName: instanceName,
	}
	b, _ := json.Marshal(&instanceMap)
	dsMetaData := models.InstanceMetaData{
		InstanceMetaData: string(b),
	}
	_, err := dsClient.RunInTransaction(context.Background(), func(tx *datastore.Transaction) error {
		key := datastore.NameKey("proxysql_metadata", "proxysql_metadata", nil)
		key.Namespace = "chester"
		_, err := tx.Put(key, &dsMetaData)
		if err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	return err
}

func getMetaData() (map[string]string, error) {
	dspsmd := models.InstanceMetaData{}
	_, err := dsClient.RunInTransaction(context.Background(), func(tx *datastore.Transaction) error {
		key := datastore.NameKey("proxysql_metadata", "proxysql_metadata", nil)
		key.Namespace = "chester"
		if err := tx.Get(key, &dspsmd); err != nil {
			return err
		}
		return nil
	})
	psmetadata := map[string]string{}
	err = json.NewDecoder(bytes.NewBuffer([]byte(dspsmd.InstanceMetaData))).Decode(&psmetadata)
	return psmetadata, err
}
