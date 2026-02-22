# Sample Data for Data Science Workspace

This directory contains sample datasets and generation scripts.

## Files

- `generate_sample_data.py` - Script to generate synthetic datasets
- `customers.csv` - Generated customer data (10k records)
- `sales.csv` - Generated sales transactions (50k records)

## Generating Data

```bash
cd /home/jovyan/work
docker-compose exec jupyter python data/generate_sample_data.py
```

Or from inside Jupyter:

```python
%run data/generate_sample_data.py
```

## Data Schema

### customers.csv
- `customer_id` - Unique identifier
- `name` - Customer name
- `email` - Email address
- `age` - Age (18-80)
- `city` - City name
- `registration_date` - When they joined

### sales.csv
- `sale_id` - Unique identifier
- `customer_id` - Reference to customers
- `product` - Product name
- `category` - Product category
- `amount` - Sale amount
- `quantity` - Units sold
- `sale_date` - Transaction date
