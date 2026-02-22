# Python Data Science Workspace

A complete data science environment demonstrating Nexus Workspace with Jupyter notebooks and persistent data storage.

## Overview

This example showcases:
- **Jupyter Lab**: Interactive notebooks for data exploration
- **Scientific Python Stack**: NumPy, Pandas, Matplotlib, Scikit-learn
- **PostgreSQL**: Database for storing and querying datasets
- **Persistent Storage**: Data volumes that survive restarts
- **Port Forwarding**: Access Jupyter from your browser

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Nexus Workspace Container               │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │                 Jupyter Lab                         │   │
│  │              (Python + ML Libraries)                │   │
│  │                     :8888                           │   │
│  │                                                      │   │
│  │  📓 notebooks/        📊 data/                      │   │
│  │  ├── 01-exploration   ├── sample.csv                │   │
│  │  ├── 02-analysis      └── generate.py               │   │
│  │  └── 03-ml-model                                     │   │
│  └─────────────────────────────────────────────────────┘   │
│                              ↕                              │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              PostgreSQL Database                    │   │
│  │                     :5432                           │   │
│  │                                                      │   │
│  │  📦 Store and query large datasets                  │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
         ↕
    Port Forwarding (:8888)
         ↕
   Host Machine Browser
```

## Project Structure

```
python-datascience-workspace/
├── docker-compose.yml          # Jupyter + PostgreSQL orchestration
├── Dockerfile                  # Data science environment
├── notebooks/                  # Jupyter notebooks
│   ├── 01-data-exploration.ipynb
│   ├── 02-statistical-analysis.ipynb
│   └── 03-machine-learning.ipynb
├── data/                       # Sample datasets
│   ├── generate_sample_data.py
│   └── README.md
├── requirements.txt            # Python dependencies
├── README.md                   # This file
└── nexus-workspace.md          # Nexus workspace guide
```

## Services

| Service | Port | Description |
|---------|------|-------------|
| Jupyter Lab | 8888 | Interactive notebooks with token authentication |
| PostgreSQL | 5432 | Database for data storage and SQL analysis |

## Included Libraries

### Core Data Science
- **pandas**: Data manipulation and analysis
- **numpy**: Numerical computing
- **matplotlib**: Static visualizations
- **seaborn**: Statistical visualizations

### Machine Learning
- **scikit-learn**: ML algorithms and preprocessing
- **xgboost**: Gradient boosting framework

### Database Connectivity
- **psycopg2**: PostgreSQL adapter
- **sqlalchemy**: SQL toolkit and ORM

### Jupyter & Utilities
- **jupyterlab**: Next-gen notebook interface
- **ipywidgets**: Interactive widgets

## Running Locally (Without Nexus)

```bash
# Navigate to example
cd examples/python-datascience-workspace

# Start services
docker-compose up -d

# View logs to get Jupyter token
docker-compose logs jupyter

# Access Jupyter Lab at: http://localhost:8888
# Token is displayed in the logs
```

## Sample Notebooks

### 1. Data Exploration (`01-data-exploration.ipynb`)
- Load and inspect datasets
- Basic statistics and profiling
- Data visualization

### 2. Statistical Analysis (`02-statistical-analysis.ipynb`)
- Hypothesis testing
- Correlation analysis
- Distribution analysis

### 3. Machine Learning (`03-machine-learning.ipynb`)
- Classification with scikit-learn
- Model evaluation
- Feature importance

## Data Generation

Generate sample datasets for testing:

```bash
# Inside the workspace
docker-compose exec jupyter python data/generate_sample_data.py

# This creates:
# - data/customers.csv (10,000 customer records)
# - data/sales.csv (50,000 sales transactions)
# - Database tables with the same data
```

## Database Integration

Connect to PostgreSQL from notebooks:

```python
import pandas as pd
from sqlalchemy import create_engine

# Connect to database
engine = create_engine('postgresql://postgres:postgres@postgres:5432/datascience')

# Query data
df = pd.read_sql('SELECT * FROM sales LIMIT 100', engine)
```

## Persistent Storage

Data is persisted across restarts:

```yaml
volumes:
  jupyter_data:
    driver: local
  postgres_data:
    driver: local
```

To reset:
```bash
docker-compose down -v
```

## Next Steps

See `nexus-workspace.md` for detailed instructions on running this in a Nexus workspace with checkpointing and port forwarding.
