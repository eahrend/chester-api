package main

// TODO: DRY this up
//  example: pubsub testing should be moved to it's own function
// 	also we need to add verbosity to the error messages
// !!!DO NOT RUN THIS FILE BY ITSELF!!!
import (
	"bytes"
	"cloud.google.com/go/datastore"
	"cloud.google.com/go/pubsub"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	models "github.com/eahrend/chestermodels"
	"github.com/stretchr/testify/assert"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// setup server side clients here here
var (
	subscription *pubsub.Subscription
	psClient     *pubsub.Client
	pubsubinit   bool
)

// tearDown resets and shuts down emulators
// also clears all client local variables
// https://github.com/googleapis/google-cloud-go/issues/224#issuecomment-218327626
func tearDown() {
	/*
		pubSubResetRequest, _ := http.NewRequest("POST", "http://localhost:8080/reset", nil)
		httpClient := &http.Client{}
		_, err := httpClient.Do(pubSubResetRequest)
		if err != nil {
			panic(err)
		}
		time.Sleep(5 *time.Second)

	*/
}

func setup() *gin.Engine {
	r := gin.Default()
	r.GET("/healthcheck", healthCheck)
	r.GET("/", healthCheck)
	r.GET("/databases/:instanceName", getDatabaseInstance)
	r.GET("/databases", getDatabases)
	r.POST("/", addDatabaseCommand)
	r.DELETE("/", removeDatabaseCommand)
	r.PATCH("/", modifyDatabaseCommand)
	r.PATCH("/queryrules/:id", modifyQueryRuleByID)
	r.PATCH("/users", modifyUser)
	r.DELETE("/users/:username", deleteUser)
	r.POST("/users", createUser)
	r.GET("/users/:username", getUser)
	r.PATCH("/key/:instanceName", updateKey)
	r.PATCH("/cert/:instanceName", updateCert)
	os.Setenv("DATASTORE_PROJECT_ID", "project-test")
	os.Setenv("DATASTORE_DATASET", "project-test")
	os.Setenv("DATASTORE_EMULATOR_HOST", "localhost:8081")
	os.Setenv("DATASTORE_EMULATOR_HOST_PATH", "localhost:8081/datastore")
	os.Setenv("DATASTORE_HOST", "http://localhost:8081")
	os.Setenv("PUBSUB_EMULATOR_HOST", "localhost:8080")
	os.Setenv("FORCE_ENCRYPT", "false")
	projectID = os.Getenv("DATASTORE_PROJECT_ID")
	var err error
	ctx = context.Background()
	dsClient, err = datastore.NewClient(ctx, os.Getenv("DATASTORE_PROJECT_ID"))
	if err != nil {
		panic(err)
	}
	psClient, err := pubsub.NewClient(ctx, os.Getenv("DATASTORE_PROJECT_ID"))
	if err != nil {
		panic(err)
	}
	// get list of topics, if one exists, just reuse it
	if !pubsubinit {
		topic, err = psClient.CreateTopic(ctx, "chester-topic")
		subscriptionConfig := pubsub.SubscriptionConfig{
			Topic:       topic,
			AckDeadline: 20 * time.Second,
		}
		subscription, err = psClient.CreateSubscription(ctx, "chester-subscription", subscriptionConfig)
		pubsubinit = true
	}
	return r
}

func TestGetHealthCheck(t *testing.T) {
	r := setup()
	defer tearDown()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/healthcheck", bytes.NewBuffer(nil))
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.FailNow()
	}
	b, _ := ioutil.ReadAll(w.Body)
	if !assert.JSONEq(t, `{"status":"ok"}`, string(b)) {
		t.FailNow()
	}
}

func TestPubSubConnectivity(t *testing.T) {
	setup()
	defer tearDown()
	ctx := context.Background()
	mesg := pubsub.Message{
		Data: []byte("foo"),
	}
	topic.Publish(ctx, &mesg)
	var err error
	cctx, cancel := context.WithCancel(ctx)
	subscription.Receive(cctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		if string(msg.Data) != "foo" {
			err = errors.New("message mismatch")
			cancel()
		} else {
			cancel()
		}
	})
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
}

func TestDatastoreConnectivity(t *testing.T) {
	setup()
	defer tearDown()
	type TestEntity struct {
		Foo string
	}
	testData := &TestEntity{Foo: "bar"}
	key := datastore.NameKey("test-key", "test-key", nil)
	key.Namespace = "test-namespace"
	_, err := dsClient.Put(ctx, key, testData)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	dataStoreEntity := &TestEntity{}
	err = dsClient.Get(ctx, key, dataStoreEntity)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if dataStoreEntity.Foo != "bar" {
		t.Error("failed to get correct datastore item")
		t.FailNow()
	}
}

func TestAddDatabase(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
	}
	queryRuleOne := models.ProxySqlMySqlQueryRule{
		RuleID:               1,
		Username:             "username",
		Active:               1,
		MatchDigest:          "^select.*",
		DestinationHostgroup: 5,
		Apply:                1,
		Comment:              "rule one",
	}
	queryRuleTwo := models.ProxySqlMySqlQueryRule{
		RuleID:               2,
		Username:             "username",
		Active:               1,
		MatchDigest:          "^delete.*",
		DestinationHostgroup: 10,
		Apply:                0,
		Comment:              "rule two",
	}
	queryRules := []models.ProxySqlMySqlQueryRule{
		queryRuleOne,
		queryRuleTwo,
	}
	addDatabaseRequest := models.AddDatabaseRequest{
		Action:       "add",
		InstanceName: "sql-instance",
		Username:     "username",
		Password:     "password",
		MasterInstance: models.AddDatabaseRequestDatabaseInformation{
			Name:      "sql-instance",
			IPAddress: "1.2.3.4",
		},
		ReadReplicas: readReplicas,
		QueryRules:   queryRules,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 3,
		},
		KeyData:   "",
		CertData:  "",
		CAData:    "",
		EnableSSL: 0,
	}
	b, _ := json.Marshal(&addDatabaseRequest)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/", bytes.NewBuffer(b))
	r.ServeHTTP(w, req)
	respBytes, err := ioutil.ReadAll(w.Body)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	expectedResponse := `{"action":"add_database","query_rules":[{"rule_id":1,"username":"username","active":1,"match_digest":"^select.*","destination_hostgroup":5,"apply":1,"comment":"rule one"},{"rule_id":2,"username":"username","active":1,"match_digest":"^delete.*","destination_hostgroup":10,"apply":0,"comment":"rule two"}],"instance_name":"sql-instance","username":"username","password":"REDACTED","write_host_group":5,"read_host_group":10,"ssl_enabled":0,"chester_meta_data":{"instance_group":"sql-instance","max_chester_instances":3}}`
	if !assert.JSONEq(t, expectedResponse, string(respBytes)) {
		t.Errorf("failed to get expected response")
		t.FailNow()
	}
	var suberr error
	cctx, cancel := context.WithCancel(ctx)
	subscription.Receive(cctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		messageBytes := msg.Data
		restartCommand := map[string]string{}
		err = json.Unmarshal(messageBytes, &restartCommand)
		if err != nil {
			suberr = err
			cancel()
		}
		if restartCommand["action"] != "restart" {
			suberr = fmt.Errorf("failed to get right action :%s", restartCommand["action"])
			cancel()
		}
		if restartCommand["sql_master_instance"] != addDatabaseRequest.InstanceName {
			suberr = fmt.Errorf("failed to get right instance name")
			cancel()
		}
		cancel()
	})
	if suberr != nil {
		t.Error(suberr)
		t.FailNow()
	}
}

func TestModifyDatabaseAddReadReplica(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	modifyReadReplicaRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		AddQueryRules:    nil,
		RemoveQueryRules: nil,
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 3,
		},
	}
	b, _ := json.Marshal(&modifyReadReplicaRequest)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(b))
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		b, _ := json.MarshalIndent(w.Body, "", "  ")
		t.Log(string(b))
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getReqOne, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getReqOne)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	instanceDataModifiedReadReplicas := models.InstanceData{}
	err := json.NewDecoder(getResponseRecorder.Body).Decode(&instanceDataModifiedReadReplicas)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if len(instanceDataModifiedReadReplicas.ReadReplicas) != 3 {
		t.Errorf("failed to add a new read replica")
		t.FailNow()
	}
}

func TestModifyDatabaseRemoveQueryRules(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	removeQueryRuleRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		AddQueryRules:    nil,
		RemoveQueryRules: []int{1},
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 3,
		},
	}
	b, _ := json.Marshal(&removeQueryRuleRequest)
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getDatabaseReq, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getDatabaseReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	r.ServeHTTP(getResponseRecorder, getDatabaseReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	removeQueryRuleData := models.InstanceData{}
	err := json.NewDecoder(getResponseRecorder.Body).Decode(&removeQueryRuleData)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if len(removeQueryRuleData.QueryRules) != 1 {
		t.Errorf("failed to remove query rule")
		t.FailNow()
	}
}

func TestModifyDatabaseAddQueryRules(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	queryRuleOne := models.ProxySqlMySqlQueryRule{
		Username:             "username",
		Active:               1,
		MatchDigest:          "^select.*",
		DestinationHostgroup: 5,
		Apply:                1,
		Comment:              "rule one",
	}
	queryRuleThree := models.ProxySqlMySqlQueryRule{
		Username:             "username",
		Active:               1,
		MatchDigest:          "^delete.*",
		DestinationHostgroup: 10,
		Apply:                0,
		Comment:              "rule three",
	}
	queryRules := []models.ProxySqlMySqlQueryRule{
		queryRuleOne,
		queryRuleThree,
	}
	addQueryRuleRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		AddQueryRules:    queryRules,
		RemoveQueryRules: nil,
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 3,
		},
	}
	addQueryRuleBytes, _ := json.Marshal(&addQueryRuleRequest)
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(addQueryRuleBytes))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getReq, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	addQueryRuleData := models.InstanceData{}
	err := json.NewDecoder(getResponseRecorder.Body).Decode(&addQueryRuleData)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if len(addQueryRuleData.QueryRules) != 3 {
		t.Errorf("failed to add query rules")
		t.FailNow()
	}
}

func TestModifyDatabaseModifyMetaData(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	modifyMetaDataRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		AddQueryRules:    nil,
		RemoveQueryRules: nil,
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 5,
		},
	}
	b, _ := json.Marshal(&modifyMetaDataRequest)
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getReq, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	modifyMetaDataData := models.InstanceData{}
	err := json.NewDecoder(getResponseRecorder.Body).Decode(&modifyMetaDataData)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if modifyMetaDataData.ChesterMetaData.MaxChesterInstances != 5 {
		t.Errorf("failed to update metadata")
		t.FailNow()
	}
}

func TestModifyDatabaseModifyUsername(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	modifyMetaDataRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		NewUsername:      "newfoo",
		AddQueryRules:    nil,
		RemoveQueryRules: nil,
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 5,
		},
	}
	b, _ := json.Marshal(&modifyMetaDataRequest)
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getReq, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	modifyUserData := models.InstanceData{}
	err := json.NewDecoder(getResponseRecorder.Body).Decode(&modifyUserData)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if modifyUserData.Username != "newfoo" {
		t.Errorf("failed to update username")
		t.FailNow()
	}
	// check datastore anyway
	psql, err := getProxySQLConfig("sql-instance")
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	// check if the query rules got updated
	if psql.MySqlQueryRules[0].Username != modifyMetaDataRequest.NewUsername {
		t.Errorf("username for query rule doesn't match")
		t.FailNow()
	}
}

func TestModifyDatabaseModifyPassword(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	modifyMetaDataRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		NewPassword:      "newbar",
		AddQueryRules:    nil,
		RemoveQueryRules: nil,
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 5,
		},
	}
	b, _ := json.Marshal(&modifyMetaDataRequest)
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getReq, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	// check datastore for the password
	psql, err := getProxySQLConfig("sql-instance")
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	// check if the query rules got updated
	if psql.MySqlUsers[0].Password != modifyMetaDataRequest.NewPassword {
		t.Errorf("password mismatch")
		t.FailNow()
	}
}

func TestModifyDatabaseModifyUserAndPassword(t *testing.T) {
	r := setup()
	defer tearDown()
	readReplicaOne := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-one",
		IPAddress: "1.2.3.5",
	}
	readReplicaTwo := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-two",
		IPAddress: "1.2.3.6",
	}
	readReplicaThree := models.AddDatabaseRequestDatabaseInformation{
		Name:      "sql-reader-three",
		IPAddress: "2.3.4.5",
	}
	readReplicas := []models.AddDatabaseRequestDatabaseInformation{
		readReplicaOne,
		readReplicaTwo,
		readReplicaThree,
	}
	modifyMetaDataRequest := models.ModifyDatabaseRequest{
		Action:           "modify",
		InstanceName:     "sql-instance",
		NewUsername:      "newshaz",
		NewPassword:      "newbot",
		AddQueryRules:    nil,
		RemoveQueryRules: nil,
		ReadReplicas:     readReplicas,
		ChesterMetaData: models.ChesterMetaData{
			InstanceGroup:       "sql-instance",
			MaxChesterInstances: 5,
		},
	}
	b, _ := json.Marshal(&modifyMetaDataRequest)
	req, _ := http.NewRequest(http.MethodPatch, "/", bytes.NewBuffer(b))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	getReq, _ := http.NewRequest(http.MethodGet, "/databases/sql-instance", nil)
	getResponseRecorder := httptest.NewRecorder()
	r.ServeHTTP(getResponseRecorder, getReq)
	if !assert.Equal(t, 200, getResponseRecorder.Code) {
		t.Errorf("unexpected return code")
		t.FailNow()
	}
	modifyUserData := models.InstanceData{}
	err := json.NewDecoder(getResponseRecorder.Body).Decode(&modifyUserData)
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	if modifyUserData.Username != "newshaz" {
		t.Errorf("failed to update username")
		t.FailNow()
	}
	// check datastore for the password
	psql, err := getProxySQLConfig("sql-instance")
	if err != nil {
		t.Error(err)
		t.FailNow()
	}
	// check if the query rules got updated
	if psql.MySqlUsers[0].Password != modifyMetaDataRequest.NewPassword {
		t.Errorf("password mismatch")
		t.FailNow()
	}
	if psql.MySqlQueryRules[0].Username != modifyMetaDataRequest.NewUsername {
		t.Errorf("username for query rule doesn't match")
		t.FailNow()
	}
}

func TestRemoveDatabase(t *testing.T) {
	r := setup()
	defer tearDown()
	removeDataBaseRequest := models.RemoveDatabaseRequest{
		Action:       "remove",
		InstanceName: "sql-instance",
		Username:     "",
	}
	b, _ := json.Marshal(&removeDataBaseRequest)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/", bytes.NewBuffer(b))
	r.ServeHTTP(w, req)
	respBytes, err := ioutil.ReadAll(w.Body)
	if err != nil {
		t.Error(string(respBytes))
		t.Error(err)
		t.FailNow()
	}
	// deleting a database doesn't return a response, just an ok
	if !assert.Equal(t, 200, w.Code) {
		t.Errorf("unexpected return code %v", w.Code)
		t.FailNow()
	}
	// check datastore just in case
	key := generateChesterKey(removeDataBaseRequest.InstanceName)
	psqlconfig := models.ProxySqlConfig{}
	err = dsClient.Get(context.Background(), key, &psqlconfig)
	if err == nil {
		t.Errorf("this should fail, since the db shouldn't exist")
		t.FailNow()
	}
	cctx, cancel := context.WithCancel(ctx)
	// check that the message was sent over pubsub
	var suberr error
	subscription.Receive(cctx, func(ctx context.Context, msg *pubsub.Message) {
		msg.Ack()
		messageBytes := msg.Data
		message := map[string]string{}
		err = json.Unmarshal(messageBytes, &message)
		if err != nil {
			suberr = err
			cancel()
		}
		if message["action"] != "restart" {
			suberr = errors.New("did not send the restart command")
			cancel()
		}
		if message["sql_master_instance"] != removeDataBaseRequest.InstanceName {
			suberr = errors.New("instance group mismatch")
			cancel()
		}
		cancel()
	})
	if suberr != nil {
		t.Error(err)
		t.FailNow()
	}
}
