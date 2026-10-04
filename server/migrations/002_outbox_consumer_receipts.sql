CREATE TABLE outbox_consumer_receipts (
    consumer_name text NOT NULL,
    event_id uuid NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer_name, event_id),
    CONSTRAINT outbox_consumer_receipts_event_fk
        FOREIGN KEY (event_id) REFERENCES outbox (event_id) ON DELETE CASCADE,
    CONSTRAINT outbox_consumer_receipts_consumer_nonblank CHECK (length(btrim(consumer_name)) > 0)
);

---- create above / drop below ----

DROP TABLE outbox_consumer_receipts;
