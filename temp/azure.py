import requests
import json

def fetch_azure_spot_prices():
    params = {
        "$filter": "serviceName eq 'Virtual Machines' and skuName eq 'E48s v6 Spot'"
    }
    try:
        url = "https://prices.azure.com/api/retail/prices"
        response = requests.get(url, params=params)

        response.raise_for_status()

        data = response.json()
        print(json.dumps(data, indent=2))

    except requests.exceptions.RequestException as e:
        print(f"Error fetching data: {e}")
    except json.JSONDecodeError as e:
        print(f"Error decoding JSON: {e}")
    except Exception as e:
        print(f"An unexpected error occurred: {e}")

fetch_azure_spot_prices()
