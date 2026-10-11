from flask import Blueprint, flash, render_template, abort, request, redirect, url_for
from jinja2 import TemplateNotFound
from flaskapp.services import speedtest_service
from flaskapp.models import DataTransformer


speedtest = Blueprint( 'speedtest', __name__, url_prefix="/speedtest" )

frequency = [
    {'id': 'all', 'text': 'All'},
    {'id': 'daily', 'text': 'Daily'},
    {'id': 'weekly', 'text': 'Weekly'},
    {'id': 'monthly', 'text': 'Monthly'},
    {'id': 'quarterly', 'text': 'Quarterly'},
    {'id': 'yearly', 'text': 'Yearly'},
]
  
@speedtest.route('/', methods=["GET"])
@speedtest.route('/')
def index():
    try:
        raw_data = speedtest_service.get_all()
        dt = DataTransformer(raw_data)
        freq = request.args.get('freq')
        freq_title = freq
        if freq == 'yearly':
            freq = 'YE'
        elif freq == 'quarterly':
            freq = 'Q'
        elif freq == 'monthly':
            freq = 'ME'
        elif freq == 'weekly':
            freq = 'W'
        elif freq == 'daily':
            freq = 'D'
        else:
            freq = None
            freq_title = "All"
        start_date = request.args.get('start_date')
        end_date = request.args.get('end_date')
        stats, data = dt.get_records(
            freq, 
            start_date, 
            end_date
        )
        return render_template(f'speedtest/index.html', stats=stats, data=data, frequency=frequency, freq_title=freq_title)
    except TemplateNotFound as e:
        abort(404)

  
# @speedtest.route('/', defaults={'page': 'index'}, methods=["GET"])
# @speedtest.route('/<page>')
# def index(page):
#     try:
#         raw_data = speedtest_service.get_all()
#         df = DataFrame(raw_data)
#         data = df.to_dict(orient='records') 
#         # data = df.to_html()
#         return render_template(f'speedtest/{page}.html', data=data)
#     except TemplateNotFound as ex:
#         abort(404)
#     except Exception as ex:
#         abort(404)

# @speedtest.errorhandler(404)
# def not_found(err):
#     return render_template('404.html')