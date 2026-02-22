-- Create tables for microservices
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(100) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Insert sample users (password: 'password123')
INSERT INTO users (name, email, password_hash) VALUES
    ('Alice Johnson', 'alice@example.com', '$2a$10$N9qo8uLOickgx2ZMRZoMy.Mqrq0YfKpQXSxF/.8LQ7J3ZP1qyQ0Q2'),
    ('Bob Smith', 'bob@example.com', '$2a$10$N9qo8uLOickgx2ZMRZoMy.Mqrq0YfKpQXSxF/.8LQ7J3ZP1qyQ0Q2'),
    ('Carol Davis', 'carol@example.com', '$2a$10$N9qo8uLOickgx2ZMRZoMy.Mqrq0YfKpQXSxF/.8LQ7J3ZP1qyQ0Q2')
ON CONFLICT (email) DO NOTHING;
