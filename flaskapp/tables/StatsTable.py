from flask_table import Table, Col


class StatsTable(Table):
    classes = ['table', 'table-striped', 'table-bordered', 'table-hover']
    local_time = Col('Local Time')
    packet_loss = Col('Packet Loss')
    download = Col('Download')
    upload = Col('Upload')