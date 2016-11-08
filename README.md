This is a stripped-down version of [patrickmn's go-cache](https://github.com/patrickmn/go-cache).

The following have been removed:
* increment and decrement functions
* serialization and deserialization
* experimental sharded cache

Other notable changes include:
* keys are now `interface{}` instead of `string`
* added `GetOrLoad` function
