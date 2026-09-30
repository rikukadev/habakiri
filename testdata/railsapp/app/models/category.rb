class Category < ApplicationRecord
  self.table_name = "taxonomy"

  def refresh_counters!
    Post.where(category_id: id).count
  end
end
