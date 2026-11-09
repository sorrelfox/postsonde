{
  "version": 1,
  "type": "collection",
  "id": "5f0c6a1e-3c1b-4f53-9d51-6f1f2a9c0b11",
  "title": "Shop API",
  "description": "GraphQL API of the shop.",
  "queries": [
    {
      "version": 1,
      "type": "window",
      "id": "q-health",
      "windowName": "Health",
      "apiUrl": "{{baseUrl}}/graphql",
      "query": "{ health }",
      "headers": [],
      "variables": "{}",
      "subscriptionUrl": "",
      "created_at": 1712345678901,
      "updated_at": 1712345678901
    },
    "not a query"
  ],
  "preRequest": {
    "script": "altair.helpers.setEnvironment('requestId', altair.helpers.uuid());",
    "enabled": true
  },
  "postRequest": {
    "script": "",
    "enabled": false
  },
  "collections": [
    {
      "id": "c-orders",
      "parentPath": "/5f0c6a1e-3c1b-4f53-9d51-6f1f2a9c0b11",
      "title": "Orders",
      "description": "",
      "queries": [
        {
          "version": 1,
          "type": "window",
          "id": "q-create-order",
          "windowName": "Create order",
          "apiUrl": "{{baseUrl}}/graphql",
          "query": "mutation CreateOrder($input: OrderInput!) {\n  createOrder(input: $input) {\n    id\n    total\n  }\n}\n",
          "headers": [
            {
              "key": "X-Request-Id",
              "value": "{{requestId}}",
              "enabled": true
            },
            {
              "key": "X-Trace",
              "value": "on",
              "enabled": false
            }
          ],
          "variables": "{\n  \"input\": {\n    \"sku\": \"{{sku}}\",\n    \"quantity\": 2\n  }\n}",
          "subscriptionUrl": "{{wsUrl}}/graphql",
          "subscriptionConnectionParams": "{}",
          "requestHandlerId": "http",
          "requestHandlerAdditionalParams": "{}",
          "subscriptionUseDefaultRequestHandler": true,
          "subscriptionRequestHandlerId": "graphql-ws",
          "preRequestScript": "",
          "preRequestScriptEnabled": false,
          "postRequestScript": "",
          "postRequestScriptEnabled": false,
          "authorizationType": "basic",
          "authorizationData": {
            "username": "{{user}}",
            "password": "hunter2"
          },
          "requestExtensions": "{\n  \"persistedQuery\": {\n    \"version\": 1,\n    \"sha256Hash\": \"abc123\"\n  }\n}",
          "fileVariables": []
        },
        {
          "version": 1,
          "type": "window",
          "id": "q-order-updates",
          "windowName": "Order updates",
          "apiUrl": "{{baseUrl}}/graphql",
          "query": "subscription OrderUpdates {\n  orderUpdated { id status }\n}\n",
          "headers": [],
          "variables": "",
          "subscriptionUrl": "wss://realtime.shop.example/graphql",
          "subscriptionConnectionParams": "{\n  \"Authorization\": \"Bearer {{token}}\"\n}",
          "requestHandlerId": "http",
          "subscriptionUseDefaultRequestHandler": false,
          "subscriptionRequestHandlerId": "app-sync",
          "authorizationType": "api-key",
          "authorizationData": {
            "headerName": "X-Api-Key",
            "headerValue": "sk-live-order-feed"
          }
        }
      ],
      "collections": [
        {
          "id": "c-returns",
          "parentPath": "/5f0c6a1e-3c1b-4f53-9d51-6f1f2a9c0b11/c-orders",
          "title": "Returns",
          "queries": [
            {
              "version": 1,
              "type": "window",
              "windowName": "",
              "apiUrl": "{{baseUrl}}/graphql",
              "query": "query ReturnReasons {\n  returnReasons\n}\n",
              "headers": [],
              "variables": "{}",
              "httpVerb": "GET",
              "authorizationType": "oauth2",
              "authorizationData": {
                "accessTokenResponse": {
                  "access_token": "oauth-token-should-not-appear"
                }
              }
            },
            {
              "version": 1,
              "type": "window",
              "windowName": "Upload label",
              "apiUrl": "{{baseUrl}}/graphql",
              "query": "mutation UploadLabel($file: Upload!, $docs: [Upload!]!) {\n  uploadLabel(file: $file, attachments: $docs)\n}\n",
              "headers": [],
              "variables": "{\n  \"file\": null,\n  \"docs\": []\n}",
              "fileVariables": [
                {
                  "name": "file",
                  "isMultiple": false,
                  "data": {}
                },
                {
                  "name": "docs",
                  "isMultiple": true,
                  "data": [{}, {}]
                },
                {
                  "name": "",
                  "isMultiple": false
                }
              ],
              "requestHandlerId": "action-cable",
              "httpVerb": "PUT"
            }
          ]
        }
      ]
    },
    {
      "id": "c-customers",
      "parentPath": "/5f0c6a1e-3c1b-4f53-9d51-6f1f2a9c0b11",
      "title": "Customers",
      "queries": [
        {
          "version": 1,
          "type": "window",
          "windowName": "Customer",
          "apiUrl": "{{baseUrl}}/graphql",
          "query": "query Customer($id: ID!) {\n  customer(id: $id) { name email }\n}\n",
          "headers": [
            {
              "key": "Authorization",
              "value": "Bearer {{token}}",
              "enabled": true
            }
          ],
          "variables": "{ \"id\": \"{{customerId}}\" }",
          "subscriptionUrl": "{{baseUrl}}/graphql",
          "authorizationType": "none",
          "authorizationData": null
        }
      ]
    }
  ]
}
