package main

import (
	"cloud.google.com/go/datastore"
	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/pubsub"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	models "github.com/eahrend/chestermodels"
	log "github.com/sirupsen/logrus"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
	"io"

	"os"
	"sort"
	"strconv"
	"strings"
)

// List of used variables across the runtime
var (
	// kmsClient is the client connection to datastore
	kmsClient *kms.KeyManagementClient
	// dsClient is the datastore client connection
	dsClient *datastore.Client
	// ctx is the context shared across the runtime
	ctx context.Context
	// topic is the pub/sub topic that the http server pushes events to
	topic *pubsub.Topic
	// projectID is the project ID that this sits in, maybe there is a way to do this without helm?
	projectID string
	// projectNumber is the project number associated with the project ID
	projectNumber string
	// backendServiceID is used the ID of the IAP enabled services
	backendServiceID string
	// allowedUsers is a csv of usernames allowed through basic auth
	allowedUsers string
	// forceEncrypt is a flag which we use to turn off encrypting data, mostly for testing, by default this is true
	forceEncrypt bool
)

// main is the entrypoint
// TODO: Move the creation of the router to it's own function, for reusability
//  in tests
func main() {
	ctx = context.Background()
	projectID = os.Getenv("PROJECT_ID")
	projectNumber = os.Getenv("PROJECT_NUMBER")
	backendServiceID = os.Getenv("BACKEND_SERVICE_ID")
	allowedUsers = os.Getenv("ALLOWED_USERS")
	datastoreOpts := option.WithCredentialsFile(os.Getenv("DATASTORE_CREDS"))
	pubSubOpts := option.WithCredentialsFile(os.Getenv("PUBSUB_CREDS"))
	var err error
	dsClient, err = datastore.NewClient(ctx, projectID, datastoreOpts)
	if err != nil {
		panic(err)
	}
	psClient, err := pubsub.NewClient(ctx, projectID, pubSubOpts)
	if err != nil {
		panic(err)
	}
	forceEncrypt = true
	if strings.ToLower(os.Getenv("FORCE_ENCRYPT")) == "false" {
		forceEncrypt = false
	}
	if forceEncrypt {
		kmsOpts := option.WithCredentialsFile(os.Getenv("KMS_CREDS"))
		kmsClient, err = kms.NewKeyManagementClient(ctx, kmsOpts)
		if err != nil {
			panic(err)
		}
	}
	if kmsClient != nil {
		defer kmsClient.Close()
	}

	topic = psClient.Topic(os.Getenv("PUBSUB_TOPIC"))
	defer dsClient.Close()
	defer psClient.Close()
	app := gin.New()
	app.Use(
		gin.LoggerWithWriter(gin.DefaultWriter, "/healthcheck"),
		gin.Recovery(),
	)
	// healthcheck endpoints aren't secured by basic auth so k8s can ping without an issue
	app.GET("/healthcheck", healthCheck)
	// healthcheck endpoints aren't secured by basic auth so k8s can ping without an issue
	app.GET("/", healthCheck)
	// Gets the database by the master instance/instance group name
	app.GET("/databases/:instanceName", authenticate(), getDatabaseInstance)
	// Gets all the databases, not currently used by clients, but why not
	app.GET("/databases", authenticate(), getDatabases)
	// Adds an instance group to datastore
	app.POST("/", authenticate(), addDatabaseCommand)
	// removes an instance group from datastore/chester
	app.DELETE("/", authenticate(), removeDatabaseCommand)
	// updates an instance group in datastore/chester
	app.PATCH("/", authenticate(), modifyDatabaseCommand)
	// TODO: Find out if we use the rest of these
	app.PATCH("/queryrules/:id", authenticate(), modifyQueryRuleByID)
	app.PATCH("/users", authenticate(), modifyUser)
	app.DELETE("/users/:username", authenticate(), deleteUser)
	app.POST("/users", authenticate(), createUser)
	app.GET("/users/:username", authenticate(), getUser)
	app.PATCH("/key/:instanceName", authenticate(), updateKey)
	app.PATCH("/cert/:instanceName", authenticate(), updateCert)
	// TODO: make the port dynamic
	panic(app.Run("0.0.0.0:8080"))
}

// not currently used
func updateKey(c *gin.Context) {
	instanceGroup := c.Param("instanceName")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": "no instance group specified",
		})
		return
	}
	keyMap := map[string]string{}
	err := json.NewDecoder(c.Request.Body).Decode(&keyMap)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		psqlconfig.KeyData = keyMap["key"]
		key := generateChesterKey(instanceGroup)
		if _, err := tx.Put(key, &psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	c.JSON(200, nil)
	return
}

// not currently used, proxysql doesn't cleanly allow for multiple certs per instance
func updateCert(c *gin.Context) {
	instanceGroup := c.Param("instanceName")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": "no instance group specified",
		})
		return
	}
	certMap := map[string]string{}
	err := json.NewDecoder(c.Request.Body).Decode(&certMap)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		psqlconfig.KeyData = certMap["cert"]
		key := generateChesterKey(instanceGroup)
		if _, err := tx.Put(key, psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	c.JSON(200, nil)
	return
}

// not currently used
func getUser(c *gin.Context) {
	userName := c.Param("username")
	instanceGroup := c.Query("instance_group")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": NOINSTANCEGROUPSPECIFIED,
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	var userData models.ProxySqlMySqlUser
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		for _, user := range psqlconfig.MySqlUsers {
			if user.Username == userName {
				userData = user
				break
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	c.JSON(200, userData)
	return
}

// TODO: Maybe deprecate this? Deleting an instancegroup will delete all associated users
func deleteUser(c *gin.Context) {
	userName := c.Param("username")
	instanceGroup := c.Query("instance_group")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": NOINSTANCEGROUPSPECIFIED,
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		newUsers := []models.ProxySqlMySqlUser{}
		for _, user := range psqlconfig.MySqlUsers {
			if user.Username != userName {
				newUsers = append(newUsers, user)
			}
		}
		psqlconfig.MySqlUsers = newUsers
		key := generateChesterKey(instanceGroup)
		if _, err := tx.Put(key, &psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	sendRestartCommand(instanceGroup)
	c.JSON(200, nil)
}

// TODO: Maybe deprecate this? We should really only have one user that goes through proxysql
func createUser(c *gin.Context) {
	newUser := models.ProxySqlMySqlUser{}
	instanceGroup := c.Query("instance_group")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": NOINSTANCEGROUPSPECIFIED,
		})
		return
	}
	err := json.NewDecoder(c.Request.Body).Decode(&newUser)
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		// get read/write host group
		readGroup := psqlconfig.ReadHostGroup
		writeGroup := psqlconfig.WriteHostGroup

		newUsers := []models.ProxySqlMySqlUser{}
		for _, user := range psqlconfig.MySqlUsers {
			if strings.ToLower(user.Username) == strings.ToLower(newUser.Username) {
				return fmt.Errorf("user already exists")
			} else {
				newUsers = append(newUsers, user)
			}
		}
		newUsers = append(newUsers, newUser)
		// update query rules
		nqrs := []models.ProxySqlMySqlQueryRule{}
		for _, rule := range psqlconfig.MySqlQueryRules {
			if rule.DestinationHostgroup == readGroup || rule.DestinationHostgroup == writeGroup {
				rule.Username = newUser.Username
				nqrs = append(nqrs, rule)
			} else {
				nqrs = append(nqrs, rule)
			}
		}
		psqlconfig.MySqlUsers = newUsers
		psqlconfig.MySqlQueryRules = nqrs
		key := generateChesterKey(instanceGroup)
		if _, err := tx.Put(key, &psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
}

// not currently used
func modifyUser(c *gin.Context) {
	modifyUserRequest := models.ModifyUserRequest{}
	instanceGroup := c.Query("instance_group")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": NOINSTANCEGROUPSPECIFIED,
		})
		return
	}
	err := json.NewDecoder(c.Request.Body).Decode(&modifyUserRequest)
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		// check to see if the user exists
		userCheck := false
		for _, user := range psqlconfig.MySqlUsers {
			if user.Username == modifyUserRequest.Username {
				userCheck = true
				break
			}
		}
		if !userCheck {
			return fmt.Errorf("user doesn't exist")
		}
		newUsers := []models.ProxySqlMySqlUser{}
		for _, user := range psqlconfig.MySqlUsers {
			if user.Username != modifyUserRequest.Username {
				newUsers = append(newUsers, user)
			} else {
				newUser := models.ProxySqlMySqlUser{}
				// check each field
				if modifyUserRequest.NewUsername != "" {
					newUser.Username = modifyUserRequest.NewUsername
				} else {
					newUser.Username = user.Username
				}
				if modifyUserRequest.Password != "" {
					newUser.Password = modifyUserRequest.Password
				} else {
					newUser.Password = user.Password
				}
				if modifyUserRequest.InstanceGroup != "" {
					newUser.InstanceGroup = modifyUserRequest.InstanceGroup
				} else {
					newUser.InstanceGroup = user.InstanceGroup
				}

				if modifyUserRequest.DefaultHostgroup != 0 {
					newUser.DefaultHostgroup = modifyUserRequest.DefaultHostgroup
				} else {
					newUser.DefaultHostgroup = user.DefaultHostgroup
				}
				newUsers = append(newUsers, newUser)
			}
		}
		psqlconfig.MySqlUsers = newUsers
		key := generateChesterKey(instanceGroup)
		if _, err := tx.Put(key, &psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	sendRestartCommand(instanceGroup)
	c.JSON(200, nil)
	return
}

// TODO: This still needs to be reworked
func modifyQueryRuleByID(c *gin.Context) {
	ruleID := c.Param("id")
	instanceGroup := c.Query("instance_group")
	if instanceGroup == "" {
		c.JSON(400, map[string]string{
			"error": NOINSTANCEGROUPSPECIFIED,
		})
	}
	ruleIDint, err := strconv.Atoi(ruleID)
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	newQR := models.ProxySqlMySqlQueryRule{}
	err = json.NewDecoder(c.Request.Body).Decode(&newQR)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	if newQR.RuleID != ruleIDint {
		c.JSON(400, map[string]string{
			"error": "rule ids dont match",
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(instanceGroup)
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		ruleCheck := false
		for _, rule := range psqlconfig.MySqlQueryRules {
			if rule.RuleID == ruleIDint {
				ruleCheck = true
				break
			}
		}
		if !ruleCheck {
			return fmt.Errorf("unable to find rule with id of %d", ruleIDint)
		}
		nqrs := []models.ProxySqlMySqlQueryRule{}
		for _, rule := range psqlconfig.MySqlQueryRules {
			if rule.RuleID != ruleIDint {
				nqrs = append(nqrs, rule)
			} else if rule.RuleID == ruleIDint {
				nqrs = append(nqrs, newQR)
			}
		}
		psqlconfig.MySqlQueryRules = nqrs
		key := generateChesterKey(instanceGroup)
		if _, err := tx.Put(key, &psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		return nil
	})
	sendRestartCommand(instanceGroup)
	c.JSON(200, nil)
}

// TODO: Modify this to be in line with our health checks
func healthCheck(c *gin.Context) {
	healthcheckResponse := map[string]string{}
	healthcheckResponse["status"] = "ok"
	c.JSON(200, healthcheckResponse)
	return
}

// Checks whether or not the request came from GCP IAP or not, as well as conditionally
// checks for basic auth
// TODO: Have a switch to disable for testing.
func authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		jwtString := c.Request.Header["X-Goog-Iap-Jwt-Assertion"]
		aud := fmt.Sprintf("/projects/%s/global/backendServices/%s", projectNumber, backendServiceID)
		token, err := idtoken.Validate(ctx, jwtString[0], aud)
		if err != nil {
			c.JSON(401, map[string]string{
				"message":  "Failed validating token",
				"audience": aud,
				"jwt":      jwtString[0],
				"error":    err.Error(),
			})
			c.Abort()
			return
		}
		// taking these out for the time being
		if token.Issuer != "https://cloud.google.com/iap" {
			c.JSON(401, map[string]string{
				"message": "token issuer issue",
				"issuer":  token.Issuer,
			})
			c.Abort()
			return
		}
		// check the list of allowed users
		allowedUsersSplit := strings.Split(allowedUsers, ",")
		userCheck := false
		for _, user := range allowedUsersSplit {
			userEmailSplit := strings.Split(c.Request.Header["X-Goog-Authenticated-User-Email"][0], ":")
			if len(userEmailSplit) == 0 {
				break
			}
			if user == userEmailSplit[1] {
				userCheck = true
				break
			}
		}
		if !userCheck {
			c.JSON(401, map[string]string{
				"message":        "failed to get allowed users",
				"user_requested": c.Request.Header["X-Goog-Authenticated-User-Email"][0],
				"usersAllowed":   allowedUsers,
			})
			c.Abort()
			return
		}
		// check here if there is a flag to turn off basic auth for testing purposes
		if os.Getenv("BASIC_AUTH_ENABLED") == "true" {
			user, pass, _ := c.Request.BasicAuth()
			// check that the user is allowed
			if user == os.Getenv("AUTH_USER") && pass == os.Getenv("AUTH_PASS") {
				c.Next()
			} else {
				c.JSON(401, map[string]string{
					"message":      "basic auth failed",
					"userProvided": user,
					"passProvided": pass,
					"userExpected": os.Getenv("AUTH_USER"),
					"passExpected": os.Getenv("AUTH_PASS"),
				})
				c.Abort()
			}
		}
		c.Next()
	}
}

// TODO: Once proxysql generates the instance:ssl model, we can add the ca/cert/key here
func modifyDatabaseCommand(c *gin.Context) {
	decoder := json.NewDecoder(c.Request.Body)
	modifyDBRequest := models.ModifyDatabaseRequest{}
	if err := decoder.Decode(&modifyDBRequest); err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	psqlconfig, err := getProxySQLConfig(modifyDBRequest.InstanceName)
	if err != nil {
		log.Error(err)
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	// TODO: This is incompatible with having multiple users, but that's not a feature getting
	//  implemented at the moment, I'll address that tech debt later.
	var username string
	// check if username/password are getting updated
	if modifyDBRequest.NewUsername == "" {
		username = psqlconfig.MySqlUsers[0].Username
	} else {
		username = modifyDBRequest.NewUsername
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		// modify the username
		newSqlUsers := []models.ProxySqlMySqlUser{}
		if modifyDBRequest.NewUsername != "" {
			newSqlUser := psqlconfig.MySqlUsers[0]
			newSqlUser.Username = modifyDBRequest.NewUsername
			newSqlUsers = append(newSqlUsers, newSqlUser)
			psqlconfig.MySqlUsers = newSqlUsers
			newQueryRules := []models.ProxySqlMySqlQueryRule{}
			for _, rule := range psqlconfig.MySqlQueryRules {
				rule.Username = modifyDBRequest.NewUsername
				newQueryRules = append(newQueryRules, rule)
			}
			psqlconfig.MySqlQueryRules = newQueryRules
		}
		// modify the password
		newerSqlUsers := []models.ProxySqlMySqlUser{}
		if modifyDBRequest.NewPassword != "" {
			newSqlUser := psqlconfig.MySqlUsers[0]
			newSqlUser.Password = modifyDBRequest.NewPassword
			newerSqlUsers = append(newerSqlUsers, newSqlUser)
			psqlconfig.MySqlUsers = newerSqlUsers
		}
		if len(modifyDBRequest.AddQueryRules) > 0 {
			log.Warnln("Adding Query Rules", len(modifyDBRequest.AddQueryRules))
			ruleIDs := []int{}
			for _, rule := range psqlconfig.MySqlQueryRules {
				ruleIDs = append(ruleIDs, rule.RuleID)
			}
			sort.Ints(ruleIDs)
			lastRuleID := ruleIDs[len(ruleIDs)-1]

			for k, rule := range modifyDBRequest.AddQueryRules {
				rule.RuleID = lastRuleID + k + 1
				rule.Username = username
				psqlconfig.MySqlQueryRules = append(psqlconfig.MySqlQueryRules, rule)
			}
		}
		// check if we're removing query rules or not
		if len(modifyDBRequest.RemoveQueryRules) > 0 {
			// Remove these rules by ID
			// ugh I hate quadratic time, find a better algorithm, it's just faster to write, and there should be a limited amount of rules
			newQueryRules := []models.ProxySqlMySqlQueryRule{}
			for _, rule := range modifyDBRequest.RemoveQueryRules {
				for _, dbRule := range psqlconfig.MySqlQueryRules {
					// if the id matches, move on to the next check
					if rule == dbRule.RuleID {
						// check that the it at least matches a destination host group
						if dbRule.DestinationHostgroup != psqlconfig.ReadHostGroup || dbRule.DestinationHostgroup != psqlconfig.WriteHostGroup {
							continue
						}
					} else {
						newQueryRules = append(newQueryRules, dbRule)
					}
				}
			}
			psqlconfig.MySqlQueryRules = newQueryRules
		}
		// modify read replica requests should always be authoritative
		servers := []models.ProxySqlMySqlServer{}
		// TODO: Maybe offer the ability to change the write server?
		// 	either, way this is the part where we add the writer and the current
		// 	read replicas added by auto-scaling
		for _, server := range psqlconfig.MySqlServers {
			if server.Hostgroup == psqlconfig.WriteHostGroup {
				servers = append(servers, server)
			} else if server.Comment == models.AddedByChester {
				servers = append(servers, server)
			}
		}
		// Coming from the client API this should only add the filtered read replicas
		//  that are created via terraform
		for _, server := range modifyDBRequest.ReadReplicas {
			rr := models.ProxySqlMySqlServer{
				Address:   server.IPAddress,
				Port:      3306,
				Hostgroup: psqlconfig.ReadHostGroup,
				// TODO: Make this dynamic
				MaxConnections: 1000,
				Comment:        server.Name,
				UseSSL:         0,
			}
			servers = append(servers, rr)
		}
		// overwrite the servers
		psqlconfig.MySqlServers = servers
		if forceEncrypt {
			err = psqlconfig.WithKMSOptsFromEnv()
			if err != nil {
				tx.Rollback()
				return err
			}
			err = psqlconfig.EncryptPasswords(kmsClient)
			if err != nil {
				tx.Rollback()
				return err
			}
		}
		// update the entry
		key := generateChesterKey(modifyDBRequest.InstanceName)
		if _, err := tx.Put(key, psqlconfig); err != nil {
			tx.Rollback()
			return err
		}
		// need to find a better way of doing this, probably make it a pointer so we can check on nil
		if modifyDBRequest.ChesterMetaData.MaxChesterInstances != 0 {
			metaDataKey := generateMetaDataKey(key)
			if _, err := tx.Put(metaDataKey, &modifyDBRequest.ChesterMetaData); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to put chestermetadata key: %s", err.Error())
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	sendRestartCommand(modifyDBRequest.InstanceName)
	c.JSON(200, nil)
}

func removeDatabaseCommand(c *gin.Context) {
	decoder := json.NewDecoder(c.Request.Body)
	removeDBRequest := models.RemoveDatabaseRequest{}
	err := decoder.Decode(&removeDBRequest)
	if err != nil {
		log.Error(err)
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = getProxySQLConfig(removeDBRequest.InstanceName)
	if err != nil {
		log.Error(err)
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		key := generateChesterKey(removeDBRequest.InstanceName)
		chesterMetaDataKey := generateMetaDataKey(key)
		keysToDelete := []*datastore.Key{
			key,
			chesterMetaDataKey,
		}
		if err := tx.DeleteMulti(keysToDelete); err != nil {
			tx.Rollback()
			return err
		}
		psqlMetaData, err := getMetaData()
		if err != nil {
			tx.Rollback()
			return err
		}
		if val, ok := psqlMetaData[removeDBRequest.InstanceName]; ok {
			delete(psqlMetaData, val)
			err = updateMetaData(psqlMetaData)
			if err != nil {
				tx.Rollback()
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Error(err)
		c.JSON(500, map[string]string{
			"error": err.Error(),
		})
		return
	}
	sendRestartCommand(removeDBRequest.InstanceName)
	c.JSON(200, nil)
}

func addDatabaseCommand(c *gin.Context) {
	decoder := json.NewDecoder(c.Request.Body)
	addDbRequest := models.AddDatabaseRequest{}
	err := decoder.Decode(&addDbRequest)
	// TODO: make these dynamic
	readHostGroupID := 10
	writeHostGroupID := 5
	if err != nil {
		log.Error()
		c.JSON(500, map[string]string{
			"error_details": err.Error(),
			"function":      "decoding json object",
		})
		return
	}
	// this is to just check if there is an error and we ignore if
	// there is no entity
	_, err = getProxySQLConfig(addDbRequest.InstanceName)
	if err != datastore.ErrNoSuchEntity && err != nil {
		c.JSON(400, map[string]string{
			"function":      "check datastore exists",
			"error_details": err.Error(),
		})
		return
	}
	userData := []models.ProxySqlMySqlUser{
		{
			Username:         addDbRequest.Username,
			Password:         addDbRequest.Password,
			DefaultHostgroup: 5,
			Active:           1,
			InstanceGroup:    addDbRequest.InstanceName,
		},
	}
	// TODO: Once proxysql adds instance:ssl config we'll add the key/ca/cert here
	serverData := []models.ProxySqlMySqlServer{}
	for _, server := range addDbRequest.ReadReplicas {
		ns := models.ProxySqlMySqlServer{
			Address:        server.IPAddress,
			Port:           3306,
			Hostgroup:      10,
			MaxConnections: 1000,
			Comment:        server.Name,
			UseSSL:         addDbRequest.EnableSSL,
		}
		serverData = append(serverData, ns)
	}
	wr := models.ProxySqlMySqlServer{
		Address:        addDbRequest.MasterInstance.IPAddress,
		Port:           3306,
		Hostgroup:      5,
		MaxConnections: 1000,
		Comment:        addDbRequest.MasterInstance.Name,
		UseSSL:         addDbRequest.EnableSSL,
	}
	serverData = append(serverData, wr)
	var qrs []models.ProxySqlMySqlQueryRule
	if addDbRequest.QueryRules == nil {
		rule1 := models.ProxySqlMySqlQueryRule{
			Username:             addDbRequest.Username,
			RuleID:               1,
			Active:               1,
			MatchDigest:          "^SELECT .* FOR UPDATE",
			DestinationHostgroup: writeHostGroupID,
			Apply:                1,
			Comment:              fmt.Sprintf("select for update goes to the writer: %s", addDbRequest.InstanceName),
		}
		rule2 := models.ProxySqlMySqlQueryRule{
			Username:             addDbRequest.Username,
			RuleID:               2,
			Active:               1,
			MatchDigest:          "^SELECT",
			DestinationHostgroup: readHostGroupID,
			Apply:                1,
			Comment:              fmt.Sprintf("selects go to the reader: %s", addDbRequest.InstanceName),
		}
		rule3 := models.ProxySqlMySqlQueryRule{
			Username:             addDbRequest.Username,
			RuleID:               3,
			Active:               1,
			MatchDigest:          ".*",
			DestinationHostgroup: writeHostGroupID,
			Apply:                1,
			Comment:              fmt.Sprintf("catch all to writer: %s", addDbRequest.InstanceName),
		}
		rule4 := models.ProxySqlMySqlQueryRule{
			Username:             addDbRequest.Username,
			RuleID:               4,
			Active:               1,
			MatchDigest:          "^DELETE",
			DestinationHostgroup: writeHostGroupID,
			Apply:                1,
			Comment:              fmt.Sprintf("deletes to writer: %s", addDbRequest.InstanceName),
		}
		qrs = append(qrs, rule1, rule2, rule3, rule4)
	} else {
		qrs = addDbRequest.QueryRules
	}
	psqlconfig := &models.ProxySqlConfig{
		DataDir: "/var/lib/proxysql",
		AdminVariables: models.ProxySqlConfigAdminVariables{
			// TODO: make this dynamic
			AdminCredentials: "proxysql-admin:adminpassw0rd",
			MysqlIFaces:      "0.0.0.0:6032",
			RefreshInterval:  2000,
		},
		MysqlVariables: models.ProxySqlConfigMysqlVariables{
			Threads:                4,
			MaxConnections:         2048,
			DefaultQueryDelay:      0,
			DefaultQueryTimeout:    36000000,
			HaveCompress:           true,
			PollTimeout:            2000,
			Interfaces:             "0.0.0.0:6033;/tmp/proxysql.sock",
			DefaultSchema:          "information_schema",
			StackSize:              1048576,
			ServerVersion:          "5.1.30",
			ConnectTimeoutServer:   10000,
			MonitorHistory:         60000,
			MonitorConnectInterval: 200000,
			MonitorPingInterval:    200000,
			PingInternalServerMsec: 10000,
			PingTimeoutServer:      200,
			CommandsStats:          true,
			SessionsSort:           true,
			MonitorUsername:        "proxysql",
			MonitorPassword:        "proxysqlpassw0rd",
		},
		MySqlServers:    serverData,
		MySqlUsers:      userData,
		MySqlQueryRules: qrs,
		ReadHostGroup:   10,
		WriteHostGroup:  5,
		// these aren't used now, but they will be.
		CAData:   addDbRequest.CAData,
		KeyData:  addDbRequest.KeyData,
		CertData: addDbRequest.CertData,
	}
	if forceEncrypt {
		err = psqlconfig.WithKMSOptsFromEnv()
		if err != nil {
			c.JSON(500, map[string]string{
				"error_details": err.Error(),
				"function":      "getting kms opts from env",
			})
			return
		}
		// check if the data about the key exists
		err = psqlconfig.EncryptPasswords(kmsClient)
		if err != nil {
			c.JSON(500, map[string]string{
				"error_details": err.Error(),
				"function":      "encrypting passwords",
			})
			return
		}
	}
	_, err = dsClient.RunInTransaction(ctx, func(tx *datastore.Transaction) error {
		key := generateChesterKey(addDbRequest.InstanceName)
		if _, err := tx.Put(key, psqlconfig); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to put proxysqlconfig key: %s", err.Error())
		}
		metaDataKey := generateMetaDataKey(key)
		if _, err := tx.Put(metaDataKey, &addDbRequest.ChesterMetaData); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to put chestermetadata key: %s", err.Error())
		}
		var psqlMetaData map[string]string
		psqlMetaData, err = getMetaData()
		if err != nil && err == io.EOF {
			err = insertMetaData(addDbRequest.InstanceName)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to create instance metadata")
			}
			psqlMetaData, err = getMetaData()
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to create instance metadata")
			}
		} else if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to get chestermetadata: %s", err.Error())
		}
		if _, ok := psqlMetaData[addDbRequest.InstanceName]; !ok {
			psqlMetaData[addDbRequest.InstanceName] = addDbRequest.InstanceName
			err = updateMetaData(psqlMetaData)
			if err != nil {
				tx.Rollback()
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(500, map[string]string{
			"error_details": err.Error(),
			"function":      "putting in transaction",
		})
		return
	}
	resp := models.AddDatabaseResponse{
		Action:          "add_database",
		QueryRules:      psqlconfig.MySqlQueryRules,
		InstanceName:    addDbRequest.InstanceName,
		Username:        addDbRequest.Username,
		Password:        "REDACTED",
		WriteHostGroup:  writeHostGroupID,
		ReadHostGroup:   readHostGroupID,
		SSLEnabled:      addDbRequest.EnableSSL,
		ChesterMetaData: addDbRequest.ChesterMetaData,
	}
	sendRestartCommand(addDbRequest.InstanceName)
	c.JSON(200, resp)
	return
}

func getDatabaseInstance(c *gin.Context) {
	instanceName := c.Param("instanceName")
	filter := c.Query("filter")
	id, err := getDatabase(instanceName, filter)
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	c.JSON(200, id)

}

func getDatabases(c *gin.Context) {
	filter := c.Query("filter")
	idr := []models.InstanceData{}
	instances, err := getMetaData()
	if err != nil {
		c.JSON(400, map[string]string{
			"error": err.Error(),
		})
		return
	}
	for _, instance := range instances {
		id, err := getDatabase(instance, filter)
		if err != nil {
			c.JSON(400, map[string]string{
				"error": err.Error(),
			})
			return
		}
		idr = append(idr, id)
	}
	c.JSON(200, idr)
}
