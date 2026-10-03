package hub

const schema = `
CREATE TABLE IF NOT EXISTS events (
	seq          INTEGER PRIMARY KEY AUTOINCREMENT,
	ts           INTEGER NOT NULL,
	topic        TEXT    NOT NULL,
	type         TEXT    NOT NULL,
	actor        TEXT    NOT NULL DEFAULT 'anonymous',
	subject      TEXT    NOT NULL DEFAULT '',
	payload_json TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS events_topic_seq   ON events(topic, seq);
CREATE INDEX IF NOT EXISTS events_subject_seq ON events(subject, seq);
CREATE TABLE IF NOT EXISTS cursors (
	seat  TEXT    NOT NULL,
	topic TEXT    NOT NULL,
	seq   INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (seat, topic)
);
`
