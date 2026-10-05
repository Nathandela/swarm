package accounts

// RecoverySchemaVersion covers the durable managed-account journal and its
// pending normalized observation inbox. Older account-capable writers must
// refuse this version rather than ignore accepted recovery evidence.
const RecoverySchemaVersion = 3
