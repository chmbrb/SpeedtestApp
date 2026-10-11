from pandas import DataFrame
from jinja2 import Environment, FileSystemLoader
from weasyprint import HTML
# from app.data import *
# from app.helpers import *
from ops.models import *

ph = ProcessHandler()
results = ph.exec_sp("spSpeedtestResults_GetAll", "US Mountain Standard Time")
# results_list = list()

# for result in results:
#     results_list.append(Speedtest.from_json(result[1]))

df = DataFrame(results)

# set date column as index
df.set_index('localtime', inplace=True)

# daily
daily_avg = df.resample('D').mean()
daily_min = df.resample('D').min()
daily_max = df.resample('D').max()

# weekly
df.resample('W').mean()

# monthly
df.resample('ME').mean()

# yearly
df.resample('YE').mean()

# pdftk
# https://python.plainenglish.io/7-free-python-pdf-libraries-you-should-know-in-2025-3eeda54188d3



# To convert a string timestamp to a datetime object in Python, use the datetime.strptime(date_string, format) method, where date_string is your timestamp string and format specifies the format of the string. For example, datetime.strptime("2022-09-19 13:55:26", "%Y-%m-%d %H:%M:%S") will convert the string to a datetime object.

# strftime is a method in Python's datetime module that formats datetime objects into readable strings based on specified format codes. You can use it to display dates and times in various formats, such as '%Y-%m-%d' for a year-month-day format.

# https://stackoverflow.com/questions/68664644/how-can-i-convert-from-utc-time-to-local-time-in-python#68664935

