package schema

// FullIntrospectionQuery asks for everything the September 2025 spec
// defines. Servers on older GraphQL libraries reject it; fall back to
// ConservativeIntrospectionQuery. It equals
// IntrospectionQuery(FullIntrospectionOptions()).
const FullIntrospectionQuery = `query IntrospectionQuery {
  __schema {
    description
    queryType { name kind }
    mutationType { name kind }
    subscriptionType { name kind }
    types {
      ...FullType
    }
    directives {
      name
      description
      isRepeatable
      locations
      args(includeDeprecated: true) {
        ...InputValue
      }
    }
  }
}

fragment FullType on __Type {
  kind
  name
  description
  specifiedByURL
  isOneOf
  fields(includeDeprecated: true) {
    name
    description
    args(includeDeprecated: true) {
      ...InputValue
    }
    type {
      ...TypeRef
    }
    isDeprecated
    deprecationReason
  }
  inputFields(includeDeprecated: true) {
    ...InputValue
  }
  interfaces {
    ...TypeRef
  }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes {
    ...TypeRef
  }
}

fragment InputValue on __InputValue {
  name
  description
  type {
    ...TypeRef
  }
  defaultValue
  isDeprecated
  deprecationReason
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
                ofType {
                  kind
                  name
                  ofType {
                    kind
                    name
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}
`

// ConservativeIntrospectionQuery asks only for fields every GraphQL server
// supports: no schema description, specifiedByURL, isRepeatable, isOneOf
// or deprecated arguments and input fields. It equals
// IntrospectionQuery(ConservativeIntrospectionOptions()).
const ConservativeIntrospectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name kind }
    mutationType { name kind }
    subscriptionType { name kind }
    types {
      ...FullType
    }
    directives {
      name
      description
      locations
      args {
        ...InputValue
      }
    }
  }
}

fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args {
      ...InputValue
    }
    type {
      ...TypeRef
    }
    isDeprecated
    deprecationReason
  }
  inputFields {
    ...InputValue
  }
  interfaces {
    ...TypeRef
  }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes {
    ...TypeRef
  }
}

fragment InputValue on __InputValue {
  name
  description
  type {
    ...TypeRef
  }
  defaultValue
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
              }
            }
          }
        }
      }
    }
  }
}
`
