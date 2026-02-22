#!/usr/bin/env python3
"""
Generate sample datasets for data science workspace.
Creates CSV files and loads data into PostgreSQL.
"""

import pandas as pd
import numpy as np
from datetime import datetime, timedelta
import random
from sqlalchemy import create_engine
import os

# Configuration
np.random.seed(42)
random.seed(42)

# Sample data
CITIES = ['New York', 'Los Angeles', 'Chicago', 'Houston', 'Phoenix', 
          'Philadelphia', 'San Antonio', 'San Diego', 'Dallas', 'San Jose']
PRODUCTS = {
    'Laptop': 'Electronics',
    'Smartphone': 'Electronics',
    'Headphones': 'Electronics',
    'Tablet': 'Electronics',
    'T-Shirt': 'Clothing',
    'Jeans': 'Clothing',
    'Sneakers': 'Clothing',
    'Jacket': 'Clothing',
    'Coffee Maker': 'Home',
    'Blender': 'Home',
    'Desk Lamp': 'Home',
    'Book Shelf': 'Home',
    'Notebook': 'Office',
    'Pen Set': 'Office',
    'Desk Organizer': 'Office',
    'Stapler': 'Office'
}

def generate_customers(n=10000):
    """Generate customer dataset."""
    print(f"Generating {n} customers...")
    
    first_names = ['James', 'Mary', 'John', 'Patricia', 'Robert', 'Jennifer', 
                   'Michael', 'Linda', 'William', 'Elizabeth', 'David', 'Barbara',
                   'Richard', 'Susan', 'Joseph', 'Jessica', 'Thomas', 'Sarah']
    last_names = ['Smith', 'Johnson', 'Williams', 'Brown', 'Jones', 'Garcia',
                  'Miller', 'Davis', 'Rodriguez', 'Martinez', 'Hernandez', 'Lopez']
    
    customers = []
    start_date = datetime(2020, 1, 1)
    end_date = datetime(2024, 1, 1)
    
    for i in range(n):
        first = random.choice(first_names)
        last = random.choice(last_names)
        name = f"{first} {last}"
        email = f"{first.lower()}.{last.lower()}{random.randint(1,999)}@example.com"
        
        customers.append({
            'customer_id': i + 1,
            'name': name,
            'email': email,
            'age': random.randint(18, 80),
            'city': random.choice(CITIES),
            'registration_date': start_date + timedelta(
                days=random.randint(0, (end_date - start_date).days)
            )
        })
    
    return pd.DataFrame(customers)

def generate_sales(customers_df, n=50000):
    """Generate sales dataset."""
    print(f"Generating {n} sales transactions...")
    
    sales = []
    start_date = datetime(2023, 1, 1)
    end_date = datetime(2024, 1, 1)
    
    for i in range(n):
        product = random.choice(list(PRODUCTS.keys()))
        category = PRODUCTS[product]
        
        # Price varies by product and category
        base_prices = {
            'Electronics': (200, 1500),
            'Clothing': (20, 200),
            'Home': (30, 300),
            'Office': (5, 50)
        }
        min_price, max_price = base_prices[category]
        amount = round(random.uniform(min_price, max_price), 2)
        
        sales.append({
            'sale_id': i + 1,
            'customer_id': random.choice(customers_df['customer_id'].values),
            'product': product,
            'category': category,
            'amount': amount,
            'quantity': random.randint(1, 5),
            'sale_date': start_date + timedelta(
                days=random.randint(0, (end_date - start_date).days),
                hours=random.randint(0, 23),
                minutes=random.randint(0, 59)
            )
        })
    
    return pd.DataFrame(sales)

def save_to_csv(customers_df, sales_df, data_dir='.'):
    """Save data to CSV files."""
    customers_path = os.path.join(data_dir, 'customers.csv')
    sales_path = os.path.join(data_dir, 'sales.csv')
    
    customers_df.to_csv(customers_path, index=False)
    sales_df.to_csv(sales_path, index=False)
    
    print(f"Saved: {customers_path} ({len(customers_df)} rows)")
    print(f"Saved: {sales_path} ({len(sales_df)} rows)")

def load_to_postgres(customers_df, sales_df):
    """Load data into PostgreSQL."""
    print("Loading data into PostgreSQL...")
    
    # Connection string
    db_url = os.getenv('DATABASE_URL', 'postgresql://postgres:postgres@postgres:5432/datascience')
    engine = create_engine(db_url)
    
    try:
        # Create tables and load data
        customers_df.to_sql('customers', engine, if_exists='replace', index=False)
        sales_df.to_sql('sales', engine, if_exists='replace', index=False)
        
        print("Data loaded into PostgreSQL successfully!")
        
        # Print summary
        with engine.connect() as conn:
            result = conn.execute("SELECT COUNT(*) FROM customers")
            customer_count = result.scalar()
            result = conn.execute("SELECT COUNT(*) FROM sales")
            sales_count = result.scalar()
            print(f"PostgreSQL tables:")
            print(f"  - customers: {customer_count} rows")
            print(f"  - sales: {sales_count} rows")
            
    except Exception as e:
        print(f"Warning: Could not load to PostgreSQL: {e}")
        print("Data saved to CSV files only.")

def main():
    """Main execution."""
    print("=" * 60)
    print("Sample Data Generator for Data Science Workspace")
    print("=" * 60)
    
    # Determine data directory
    script_dir = os.path.dirname(os.path.abspath(__file__))
    data_dir = script_dir
    
    # Generate data
    customers_df = generate_customers(10000)
    sales_df = generate_sales(customers_df, 50000)
    
    # Save to CSV
    save_to_csv(customers_df, sales_df, data_dir)
    
    # Try to load to PostgreSQL
    load_to_postgres(customers_df, sales_df)
    
    print("=" * 60)
    print("Data generation complete!")
    print("=" * 60)

if __name__ == '__main__':
    main()
