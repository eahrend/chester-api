# Chester API

## TODO
* Allow for more dynamic creations, rather than the defaults

## Configuration
* PROJECT_ID - GCP Project ID
* PROJECT_NUMBER - Project number for the project ID, this isn't easily accessible through the UI, but you can get it via gcloud and jq. `$ gcloud projects describe $PROJECT_ID --format=json | jq .projectNumber`
* BACKEND_SERVICE_ID - The ID of the backend service where we get the jwt assertion.
* ALLOWED_USERS - CSV of usernames for basic auth
* DATASTORE_CREDS - Physical location of the json token that we use to authenticate to datastore.
* PUBSUB_CREDS - Physical location of the json token that we use to authenticate to pub/sub
* PUBSUB_TOPIC - Name of the topic that we send details over to.
* BASIC_AUTH_ENABLED - true/false, helps with debugging.
* AUTH_USER - Service account username used to authenticate
* AUTH_PASS - password for service account used to authenticate
* KMS_CREDS - cred file used to access an asymmetrical decrypt key in GCP

### Authz
Chester's API sits behind google's identity aware proxy, and as such leverages it to confirm your identity. In addition, it uses basic auth as a failsafe in case IAP goes down.

### Adding a database
This will be the request/response body for creating new database entries for the configmap/datastore. These use default read/write split options. If you want to create more complex ones you need to modify them afterwards, since we don't know what the host group numbers are.

REQUEST:
```json
{
  "action": "add_database",
  "instance_name": "sql-instance",
  "username": "sql-user",
  "password": "abcdef",
  "master_instance": {
    "name": "sql-instance-writer",
    "ip_address": "1.2.3.4"
  },
  "read_replicas": [
    {
      "name": "sql-instance-reader-1",
      "ip_address": "1.2.3.5"
    },
    {
      "name": "sql-instance-reader-2",
      "ip_address": "1.2.3.6"
    }
  ]
}
```

RESPONSE:

200:
```json
{
  "action": "add_database",
  "instance_name": "sql-instance",
  "username": "sql-user",
  "password": "REDACTED",
  "write_host_group": 1,
  "read_host_group": 2 
}
```


### Modifying an instance group
Query rules here are considered absolute. Include everything or consider anything left out removable 

REQUEST:
```json
{
  "action": "modify_database",
  "instance_name": "sql-instance",
  "new_username": "sql-user",
  "new_password": "foo",
  "query_rules": [
    {
      "active": 1,
      "match_digest": "^SELECT .* FOR UPDATE",
      "destination_hostgroup":6,
      "apply": 1,
      "comment": "foobar"
    } 
  ]
}
```

RESPONSE:

200 - Done

400 - User Error

500 - Server Error



### Deleting an instance group
Just specify the instance group and user that you want deleted and it will do it

```json
{
  "action": "delete_database",
  "instance_name": "sql-instance",
  "username": "sql-instance-user"
}
```

## Local development
It's kind of a drag cause there aren't any mock server libraries at the moment. I'll work on docker later but here is the first part

Get gcloud beta, and run the following commands to get datastore + pubsub emulation stood up

`gcloud beta emulators pubsub start --host-port=localhost`
`gcloud beta emulators datastore start --host-port=localhost`

Once you're done with your tests you can clear pubsub via:
`curl --location --request POST 'http://localhost:8080/reset'`