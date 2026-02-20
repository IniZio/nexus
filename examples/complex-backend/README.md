# Complex Backend (Express + PostgreSQL)

A full-featured backend with database operations for Nexus test drives.

## Quick Start

```bash
cd examples/complex-backend
npm install
cp .env.example .env
# Edit .env with your DATABASE_URL
npm run migrate
npm start
```

## Database Setup

```bash
# Create PostgreSQL database
createdb nexus_test

# Run migrations
npm run migrate
```

## API Endpoints

### Users
- `GET /api/users` - List all users
- `GET /api/users/:id` - Get user by ID
- `POST /api/users` - Create user
- `PUT /api/users/:id` - Update user (admin)
- `DELETE /api/users/:id` - Delete user (admin)

### Products
- `GET /api/products` - List products (with filters)
- `GET /api/products/:id` - Get product by ID
- `POST /api/products` - Create product (admin)
- `PUT /api/products/:id` - Update product (admin)
- `DELETE /api/products/:id` - Delete product (admin)

### Orders
- `GET /api/orders` - List orders
- `GET /api/orders/:id` - Get order with items
- `POST /api/orders` - Create order
- `PATCH /api/orders/:id/status` - Update order status

## Test Scenarios

See `nexus-test-plan.md` for comprehensive test scenarios.
