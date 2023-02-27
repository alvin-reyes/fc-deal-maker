# Δ Delta
Generic DealMaking MicroService using whypfs + filclient + estuary_auth

![image](https://user-images.githubusercontent.com/4479171/218267752-9a7af133-4e36-4f4c-95da-16b3c7bd73ae.png)


## Features
- Creates a deal for large files. The recommended size is 1GB. 
- Shows all the deals made for specific user

This is strictly a deal-making service. It'll pin the files/CIDs but it won't keep the files. Once a deal is made, the CID will be removed from the blockstore. For retrievals, use the retrieval-deal microservice.

## Process Flow
- client upload files or specifies a pre-computed piece_commitments
- service queues the request for content or commp
- dispatcher runs every N seconds to check the request

## Configuration

Create the .env file in the root directory of the project. The following are the required fields.
```
# Node info
NODE_NAME=stg-deal-maker
NODE_DESCRIPTION=Experimental Deal Maker
NODE_TYPE=delta-main

# Database configuration
MODE=standalone
DB_DSN=stg-deal-maker
#REPO=/mnt/.whypfs # shared mounted repo

# Frequencies
MAX_CLEANUP_WORKERS=1500
```

Running this the first time will generate a wallet. Make sure to get FIL from the [faucet](https://verify.glif.io/) and fund the wallet




## Install the following pre-req
- go 1.18
- [jq](https://stedolan.github.io/jq/)
- [hwloc](https://www.open-mpi.org/projects/hwloc/)
- opencl
- [rustup](https://rustup.rs/)
- postgresql

Alternatively, if using Ubuntu, you can run the following commands to install the pre-reqs
```
apt-get update && \
apt-get install -y wget jq hwloc ocl-icd-opencl-dev git libhwloc-dev pkg-config make && \
apt-get install -y cargo
```

## Build and run

### Using `make` lang
```
make all
./delta daemon --repo=.whypfs --wallet-dir=<walletdir>
```

### Using `go` lang
```
go build -tags netgo -ldflags '-s -w' -o delta
./delta daemon --repo=.whypfs --wallet-dir=<walletdir>
```

### Using `docker`
```
docker build -t delta .
docker run -it --rm -p 1414:1414 delta --repo=.whypfs --wallet-dir=<walletdir>
```

## REST API Endpoints

### Node information
To get the node information, use the following endpoints
```
curl --location --request GET 'http://localhost:1414/open/node/info'
curl --location --request GET 'http://localhost:1414/open/node/peers'
curl --location --request GET 'http://localhost:1414/open/node/host'
```

### Upload a file
Use the following endpoint to upload a file. The process will automatically compute the piece size and initiate the deal proposal
and transfer
- miner is required
- connection_mode is optional. Default is `e2e` (formerly known as online deals)
- 
```
curl --location --request POST 'http://localhost:1414/api/v1/deal/content' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]' \
--form 'data=@"/Users/alvinreyes/Downloads/baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq.car"' \
--form 'metadata="{\"miner\":\"f01963614\",\"connection_mode\":\"e2e\"}"'
```

### Import mode (formerly known as offline deal)
Use the following endpoint to upload a file with a specific miner, duration, piece size and connection mode.
```
curl --location --request POST 'http://localhost:1414/api/v1/deal/piece-commitment' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]' \
--header 'Content-Type: application/json' \
--data-raw '{
    "cid": "bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
    "miner":"f01963614",
    "piece_commitment": {
        "piece_cid": "baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
        "padded_piece_size": 4294967296
    },
    "connection_mode": "import",
    "size": 2500366291,
    "remove_unsealed_copies":true, 
    "skip_ipni_announce": true
}'
```

### Batch Import mode (formerly known as offline deals)
The request body is an array of objects. The following is an example of a batch import request.
```
curl --location --request POST 'http://localhost:1414/api/v1/deal/piece-commitments' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]' \
--header 'Content-Type: application/json' \
--data-raw '[{
    "cid": "bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
    "miner":"f01963614",
    "piece_commitment": {
        "piece_cid": "baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
        "padded_piece_size": 4294967296
    },
    "connection_mode": "import",
    "size": 2500366291,
    "remove_unsealed_copies":true, 
    "skip_ipni_announce": true
},
{
    "cid": "bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
    "miner":"f01963614",
    "piece_commitment": {
        "piece_cid": "baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
        "padded_piece_size": 4294967296
    },
    "connection_mode": "import",
    "size": 2500366291,
    "remove_unsealed_copies":true, 
    "skip_ipni_announce": true
},
{
    "cid": "bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
    "miner":"f01963614",
    "piece_commitment": {
        "piece_cid": "baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
        "padded_piece_size": 4294967296
    },
    "connection_mode": "import",
    "size": 2500366291,
    "remove_unsealed_copies":true, 
    "skip_ipni_announce": true
}]'
```

### Stats (content, commps and deals)
```
curl --location --request GET 'http://localhost:1414/api/v1/stats' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]' \
```

### Stats of a specific content
When you upload, it returns a content id, use that to get the stats of a specific content
```
curl --location --request GET 'http://localhost:1414/api/v1/stats/content/1' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]'
```
### Import a wallet and use it to make a deal
```
curl --location --request POST 'http://localhost:1414/admin/wallet/register' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]' \
--header 'Content-Type: application/json' \
--data-raw '{
"address":"<public key>",
"key_type":"bls",
"private_key":"<private_key>"
}'
```

Get the response and use the wallet id to make a deal
```
{
    "message": "Successfully imported a wallet address. Please take note of the UUID.",
    "wallet_addr": "<public address>",
    "wallet_uuid": "ff0e8640-b662-11ed-860a-9e0bf0c70138"
}
```

### Make a deal using the registered wallet
```
curl --location --request POST 'http://shuttle-4-bs1.estuary.tech:1414/api/v1/deal/piece-commitments' \
--header 'Authorization: Bearer [ESTUARY_API_KEY]' \
--header 'Content-Type: application/json' \
--data-raw '[{
    "cid": "bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
    "miner":"f01963614",
    "wallet": {
        "address":"<public address after registering>",
        "uuid":"<wallet id after registering>" // optional
    },
    "piece_commitment": {
        "piece_cid": "baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
        "padded_piece_size": 4294967296
    },
    "connection_mode": "import",
    "size": 2500366291,
    "remove_unsealed_copies":true, 
    "skip_ipni_announce": true
}]'
```

## CLI
### Get the commp of a file using commp cli
```
./delta commp --file=<>
```

### Get the commp of a CAR file using commp cli
```
./delta commp-car --file=<>
```

if you want to get the commp of a CAR file for offline deal, use the following command
```
./delta commp-car --file=<> --for-import
```
The output will be as follows
```
{
    "cid": "bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
    "wallet": {},
    "commp": {
        "piece": "baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
        "padded_piece_size": 4294967296
    },
    "connection_mode": "import",
    "size": 2500366291
}
```

### Get the commp of a CAR file using commp cli and pass to the delta api to make an offline deal
```
./delta commp-car --file=baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq.car --for-offline --delta-api-url=http://localhost:1414 --delta-api-key=[ESTUARY_API_KEY]
```

Output
```
{
   "status":"success",
   "message":"File uploaded and pinned successfully",
   "content_id":208,
   "piece_commitment_id":172,
   "meta":{
      "cid":"bafybeidty2dovweduzsne3kkeeg3tllvxd6nc2ifh6ztexvy4krc5pe7om",
      "wallet":{
         
      },
      "commp":{
         "piece":"baga6ea4seaqhfvwbdypebhffobtxjyp4gunwgwy2ydanlvbe6uizm5hlccxqmeq",
         "padded_piece_size":4294967296,
         "unpadded_piece_size":4261412864
      },
      "connection_mode":"import",
      "size":2500366291
   }
}
```

## Author
Outercore Engineering Team.