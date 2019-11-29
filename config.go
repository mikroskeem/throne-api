package main

type throneAPIConfig struct {
	RestAPI  restAPIConfig        `toml:"rest_api"`
	Database throneDatabaseConfig `toml:"database"`
}

type restAPIConfig struct {
	ListenAddress string `toml:"listen_address"`
	CORSOrigins   string `toml:"cors_origin"`
}

type throneDatabaseConfig struct {
	DatabaseURL             string   `toml:"database_url"`
	LuckPermsDatabaseName   string   `toml:"luckperms_database_name"`
	LuckPermsTablePrefix    string   `toml:"luckperms_table_prefix"`
	ConfettiDatabaseName    string   `toml:"confetti_database_name"`
	ConfettiVotesTableName  string   `toml:"confetti_votes_table_name"`
	StaffGroupNames         []string `toml:"staff_group_names"`
	BenjiAuthDatabaseName   string   `toml:"benjiauth_database_name"`
	BenjiAuthUsersTableName string   `toml:"benjiauth_users_table_name"`
}
