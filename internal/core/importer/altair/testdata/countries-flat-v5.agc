{
  "version": 1,
  "type": "collection",
  "id": 3,
  "title": "Countries",
  "queries": [
    {
      "version": 1,
      "type": "window",
      "windowName": "All countries",
      "apiUrl": "https://countries.example/graphql",
      "query": "{ countries { code name } }",
      "headers": [{"key": "Accept", "value": "application/json", "enabled": true}],
      "variables": "{}",
      "subscriptionUrl": "",
      "preRequestScript": "",
      "preRequestScriptEnabled": false
    }
  ],
  "collections": [
    {
      "id": 7,
      "parentPath": "/3/4",
      "title": "Cities",
      "queries": [
        {
          "windowName": "City",
          "apiUrl": "https://countries.example/graphql",
          "query": "query City($id: ID!) { city(id: $id) { name } }",
          "variables": "{\"id\": \"{{cityId}}\"}"
        }
      ]
    },
    {
      "id": 4,
      "parentPath": "/3",
      "title": "Continents",
      "queries": []
    },
    {
      "id": 9,
      "parentPath": "/3/99",
      "title": "Lost",
      "queries": []
    },
    {
      "id": 11,
      "parentPath": "/3/12",
      "title": "Loop A",
      "queries": []
    },
    {
      "id": 12,
      "parentPath": "/3/11",
      "title": "Loop B",
      "queries": []
    }
  ]
}
